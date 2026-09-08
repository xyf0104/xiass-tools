//go:build darwin

package patcher

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

var zipEndOfCentralDirectorySignature = []byte{'P', 'K', 0x05, 0x06}

type agentEmbeddedUIArchive struct {
	start       int
	end         int
	stylesheets []string
	reader      *zip.Reader
}

var agentEmbeddedUIStylesheets = []string{"compiled_tailwind.css", "jetbox.css"}

// readAgentEmbeddedUIArchive locates the self-contained ZIP resource embedded
// in the Agent language server. The archive is followed by other PE data, so
// archive/zip cannot discover it by treating the complete executable as ZIP.
func readAgentEmbeddedUIArchive(data []byte) (*agentEmbeddedUIArchive, error) {
	searchEnd := len(data)
	for searchEnd >= len(zipEndOfCentralDirectorySignature) {
		relative := bytes.LastIndex(data[:searchEnd], zipEndOfCentralDirectorySignature)
		if relative < 0 {
			break
		}
		searchEnd = relative
		if relative+22 > len(data) {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(data[relative+20 : relative+22]))
		archiveEnd := relative + 22 + commentLength
		if archiveEnd > len(data) {
			continue
		}
		centralSize := int(binary.LittleEndian.Uint32(data[relative+12 : relative+16]))
		centralOffset := int(binary.LittleEndian.Uint32(data[relative+16 : relative+20]))
		archiveStart := relative - centralSize - centralOffset
		if archiveStart < 0 || archiveStart >= archiveEnd {
			continue
		}
		segment := data[archiveStart:archiveEnd]
		reader, err := zip.NewReader(bytes.NewReader(segment), int64(len(segment)))
		if err != nil {
			continue
		}
		required := map[string]bool{"index.html": false, "main.js": false}
		stylesheetEntries := make(map[string]bool, len(agentEmbeddedUIStylesheets))
		duplicateRequired := false
		duplicateStylesheet := false
		for _, entry := range reader.File {
			if _, ok := required[entry.Name]; ok {
				duplicateRequired = duplicateRequired || required[entry.Name]
				required[entry.Name] = true
			}
			for _, candidate := range agentEmbeddedUIStylesheets {
				if entry.Name == candidate {
					duplicateStylesheet = duplicateStylesheet || stylesheetEntries[candidate]
					stylesheetEntries[candidate] = true
				}
			}
		}
		var stylesheets []string
		for _, candidate := range agentEmbeddedUIStylesheets {
			if stylesheetEntries[candidate] {
				stylesheets = append(stylesheets, candidate)
			}
		}
		if required["index.html"] && required["main.js"] && len(stylesheets) > 0 && !duplicateRequired && !duplicateStylesheet {
			return &agentEmbeddedUIArchive{start: archiveStart, end: archiveEnd, stylesheets: stylesheets, reader: reader}, nil
		}
	}
	return nil, fmt.Errorf("Agent Language Server 中未找到经过验证的内嵌 UI 资源包")
}

func readAgentEmbeddedUIEntry(archive *agentEmbeddedUIArchive, name string) ([]byte, error) {
	for _, entry := range archive.reader.File {
		if entry.Name != name {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return data, nil
	}
	return nil, fmt.Errorf("Agent 内嵌 UI 缺少 %s", name)
}
