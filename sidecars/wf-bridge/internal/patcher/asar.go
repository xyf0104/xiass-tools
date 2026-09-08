package patcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const integrityBlockSize = 4 * 1024 * 1024

type asarIntegrity struct {
	Algorithm string   `json:"algorithm"`
	Hash      string   `json:"hash"`
	BlockSize int      `json:"blockSize"`
	Blocks    []string `json:"blocks"`
}

type asarNode struct {
	Files      map[string]*asarNode `json:"files,omitempty"`
	Size       *int64               `json:"size,omitempty"`
	Offset     string               `json:"offset,omitempty"`
	Unpacked   bool                 `json:"unpacked,omitempty"`
	Executable bool                 `json:"executable,omitempty"`
	Link       string               `json:"link,omitempty"`
	Integrity  *asarIntegrity       `json:"integrity,omitempty"`
}

type asarArchive struct {
	path       string
	headerSize int64
	root       *asarNode
}

func align4(value int) int { return (value + 3) &^ 3 }

func readASAR(path string) (*asarArchive, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var prefix [8]byte
	if _, err := io.ReadFull(file, prefix[:]); err != nil {
		return nil, fmt.Errorf("读取 ASAR 头失败: %w", err)
	}
	headerSize := int64(readU32(prefix[4:8]))
	if headerSize < 8 || headerSize > 64*1024*1024 {
		return nil, fmt.Errorf("ASAR 头长度异常: %d", headerSize)
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(file, header); err != nil {
		return nil, fmt.Errorf("读取 ASAR 元数据失败: %w", err)
	}
	if len(header) < 8 {
		return nil, errors.New("ASAR 元数据过短")
	}
	jsonLength := int(readU32(header[4:8]))
	if jsonLength < 0 || 8+jsonLength > len(header) {
		return nil, errors.New("ASAR JSON 长度异常")
	}
	var root asarNode
	if err := json.Unmarshal(header[8:8+jsonLength], &root); err != nil {
		return nil, fmt.Errorf("解析 ASAR 元数据失败: %w", err)
	}
	if root.Files == nil {
		return nil, errors.New("ASAR 根目录无文件表")
	}
	return &asarArchive{path: path, headerSize: headerSize, root: &root}, nil
}

func (archive *asarArchive) node(name string) (*asarNode, error) {
	node := archive.root
	for _, part := range strings.Split(strings.Trim(name, "/"), "/") {
		if part == "" || part == "." || part == ".." || node.Files == nil || node.Files[part] == nil {
			return nil, fmt.Errorf("app.asar 内找不到: %s", name)
		}
		node = node.Files[part]
	}
	return node, nil
}

func (archive *asarArchive) readFile(name string) ([]byte, error) {
	node, err := archive.node(name)
	if err != nil {
		return nil, err
	}
	if node.Unpacked {
		return os.ReadFile(filepath.Join(archive.path+".unpacked", filepath.FromSlash(name)))
	}
	offset, err := strconv.ParseInt(node.Offset, 10, 64)
	if err != nil || offset < 0 || node.Size == nil || *node.Size < 0 {
		return nil, fmt.Errorf("ASAR 文件偏移异常: %s", name)
	}
	file, err := os.Open(archive.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data := make([]byte, *node.Size)
	if _, err := file.ReadAt(data, 8+archive.headerSize+offset); err != nil {
		return nil, fmt.Errorf("读取 ASAR 文件 %s 失败: %w", name, err)
	}
	return data, nil
}

func asarFileIntegrity(data []byte) *asarIntegrity {
	sum := sha256.Sum256(data)
	blocks := make([]string, 0, (len(data)+integrityBlockSize-1)/integrityBlockSize)
	for start := 0; start < len(data); start += integrityBlockSize {
		end := start + integrityBlockSize
		if end > len(data) {
			end = len(data)
		}
		block := sha256.Sum256(data[start:end])
		blocks = append(blocks, hex.EncodeToString(block[:]))
	}
	if len(blocks) == 0 {
		blocks = append(blocks, hex.EncodeToString(sum[:]))
	}
	return &asarIntegrity{Algorithm: "SHA256", Hash: hex.EncodeToString(sum[:]), BlockSize: integrityBlockSize, Blocks: blocks}
}

func (archive *asarArchive) putFile(name string, data []byte) error {
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return errors.New("ASAR 文件名为空")
	}
	node := archive.root
	for _, part := range parts[:len(parts)-1] {
		if node.Files == nil {
			node.Files = map[string]*asarNode{}
		}
		child := node.Files[part]
		if child == nil {
			child = &asarNode{Files: map[string]*asarNode{}}
			node.Files[part] = child
		}
		node = child
	}
	if node.Files == nil {
		node.Files = map[string]*asarNode{}
	}
	size := int64(len(data))
	node.Files[parts[len(parts)-1]] = &asarNode{Size: &size, Integrity: asarFileIntegrity(data)}
	return nil
}

type asarPackedFile struct {
	name string
	node *asarNode
	data []byte
}

func (archive *asarArchive) write(path string, replacements map[string][]byte) error {
	for name, data := range replacements {
		if err := archive.putFile(name, data); err != nil {
			return err
		}
	}
	var packed []asarPackedFile
	var walk func(string, *asarNode) error
	walk = func(base string, node *asarNode) error {
		keys := make([]string, 0, len(node.Files))
		for key := range node.Files {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := node.Files[key]
			name := strings.TrimPrefix(base+"/"+key, "/")
			if child.Files != nil {
				if err := walk(name, child); err != nil {
					return err
				}
				continue
			}
			if child.Unpacked || child.Link != "" {
				continue
			}
			data, ok := replacements[name]
			if !ok {
				var err error
				data, err = archive.readFile(name)
				if err != nil {
					return err
				}
			}
			packed = append(packed, asarPackedFile{name: name, node: child, data: data})
		}
		return nil
	}
	if err := walk("", archive.root); err != nil {
		return err
	}
	var offset int64
	for index := range packed {
		packed[index].node.Offset = strconv.FormatInt(offset, 10)
		size := int64(len(packed[index].data))
		packed[index].node.Size = &size
		packed[index].node.Integrity = asarFileIntegrity(packed[index].data)
		offset += size
	}
	headerJSON, err := json.Marshal(archive.root)
	if err != nil {
		return err
	}
	headerPayload := make([]byte, 4+align4(4+len(headerJSON)))
	putU32(headerPayload[0:4], uint32(align4(4+len(headerJSON))))
	putU32(headerPayload[4:8], uint32(len(headerJSON)))
	copy(headerPayload[8:], headerJSON)
	sizePickle := make([]byte, 8)
	putU32(sizePickle[0:4], 4)
	putU32(sizePickle[4:8], uint32(len(headerPayload)))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(sizePickle); err != nil {
		return err
	}
	if _, err = file.Write(headerPayload); err != nil {
		return err
	}
	for _, item := range packed {
		if _, err = file.Write(item.data); err != nil {
			return err
		}
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func readU32(source []byte) uint32 {
	return uint32(source[0]) | uint32(source[1])<<8 | uint32(source[2])<<16 | uint32(source[3])<<24
}

func putU32(destination []byte, value uint32) {
	destination[0], destination[1], destination[2], destination[3] = byte(value), byte(value>>8), byte(value>>16), byte(value>>24)
}
