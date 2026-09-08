//go:build darwin

package patcher

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfficialDarwinAgent260EmbeddedUIWhenFixturePresent is deliberately
// opt-in: the application in /Applications remains read-only.  Every mutated
// byte is first prepared in memory and is then written only to an isolated
// single-file copy under t.TempDir().
func TestOfficialDarwinAgent260EmbeddedUIWhenFixturePresent(t *testing.T) {
	appRoot := strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT"))
	if appRoot == "" {
		t.Skip("set ANTIGRAVITY_WF_TEST_DARWIN_AGENT_ROOT to an official read-only Antigravity 2.0.app")
	}

	target, ok := inspectDarwinApp(appRoot)
	if !ok || target.kind != "agent" {
		t.Fatalf("official standalone app was not detected as Agent: ok=%t target=%+v", ok, target)
	}
	wantLanguage := filepath.Join(filepath.Clean(appRoot), "Contents", "Resources", "bin", "language_server")
	if filepath.Clean(target.language) != wantLanguage {
		t.Fatalf("official Agent language server = %q, want %q", target.language, wantLanguage)
	}

	original, err := os.ReadFile(target.language)
	if err != nil {
		t.Fatal(err)
	}
	if got := countVerifiedDarwinAgentUIArchives(original); got != 1 {
		t.Fatalf("verified embedded Agent UI ZIP archives = %d, want exactly 1", got)
	}

	archive, err := readAgentEmbeddedUIArchive(original)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(archive.stylesheets, ","), "compiled_tailwind.css,jetbox.css"; got != want {
		t.Fatalf("official 2.6.0 stylesheet layout = %q, want mixed layout %q", got, want)
	}
	mainData, err := readAgentEmbeddedUIEntry(archive, "main.js")
	if err != nil {
		t.Fatal(err)
	}
	mainSource := string(mainData)
	for _, required := range []string{"Generated with", "Generated image preview", "Artifact image", "gemini-3.1-flash-image"} {
		if !strings.Contains(mainSource, required) {
			t.Fatalf("official Agent main.js is missing verified structure %q", required)
		}
	}
	if _, result := patchAgentImageUI(mainSource); !result.Recognized || !result.Changed {
		t.Fatalf("official Agent main.js matchers did not recognize a clean patchable UI: %+v", result)
	}

	// Bind the real 2.6.0 fixture to the production plan entry point.  The plan
	// targets a disposable one-file copy, never the application in /Applications.
	copyPath := filepath.Join(t.TempDir(), "language_server")
	info, err := os.Stat(target.language)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, original, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	basePlan := &patchPlan{
		path:     copyPath,
		original: original,
		updated:  append([]byte(nil), original...),
		mode:     info.Mode(),
	}
	plan, archiveRecognized, err := prepareDarwinAgentEmbeddedUIPlan(basePlan)
	if err != nil {
		t.Fatalf("prepare official Agent embedded UI plan: %v", err)
	}
	if !archiveRecognized || plan == nil || !plan.changed {
		t.Fatalf("official Agent UI plan was not recognized as a changed production plan: recognized=%t plan=%+v", archiveRecognized, plan)
	}
	updated := plan.updated
	if len(updated) != len(original) {
		t.Fatalf("patched Mach-O length = %d, want %d", len(updated), len(original))
	}
	if !bytes.Equal(updated[:archive.start], original[:archive.start]) ||
		!bytes.Equal(updated[archive.end:], original[archive.end:]) {
		t.Fatal("Agent UI patch changed Mach-O bytes outside the unique embedded ZIP")
	}

	patchedArchive, err := readAgentEmbeddedUIArchive(updated)
	if err != nil {
		t.Fatalf("re-read patched Agent UI archive: %v", err)
	}
	patchedMain, err := readAgentEmbeddedUIEntry(patchedArchive, "main.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		agentImageGenerationUIPatchMarker,
		agentImageGenerationDedupePatchMarker,
		imageGenerationUIPatchMarker,
	} {
		if !bytes.Contains(patchedMain, []byte(marker)) {
			t.Fatalf("re-read patched Agent main.js is missing %s", marker)
		}
	}
	t.Run("patched main.js passes node --check", func(t *testing.T) {
		assertDarwinAgentMainNodeCheck(t, patchedMain)
	})

	// Exercise the production write path against that same disposable copy.
	// This proves the prepared real-byte plan is atomically applicable without
	// ever modifying the application supplied through the opt-in fixture.
	if err := writePatchPlans([]*patchPlan{plan}); err != nil {
		t.Fatalf("apply Agent UI plan to disposable Language Server copy: %v", err)
	}
	written, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, updated) {
		t.Fatal("disposable Language Server did not receive the prepared byte-exact plan")
	}
	writtenArchive, err := readAgentEmbeddedUIArchive(written)
	if err != nil {
		t.Fatal(err)
	}
	writtenMain, err := readAgentEmbeddedUIEntry(writtenArchive, "main.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{agentImageGenerationUIPatchMarker, agentImageGenerationDedupePatchMarker} {
		if !bytes.Contains(writtenMain, []byte(marker)) {
			t.Fatalf("disposable plan output is missing %s", marker)
		}
	}
}

func assertDarwinAgentMainNodeCheck(t *testing.T, mainData []byte) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; patched Agent main.js syntax check skipped")
	}
	path := filepath.Join(t.TempDir(), "main.js")
	if err := os.WriteFile(path, mainData, 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("patched official Agent main.js failed node --check: %s: %v", output, err)
	}
}

// countVerifiedDarwinAgentUIArchives independently enumerates every valid
// classic ZIP candidate in the Mach-O.  readAgentEmbeddedUIArchive returns one
// safe archive; this helper additionally proves that the official source does
// not contain a second equally plausible UI archive.
func countVerifiedDarwinAgentUIArchives(data []byte) int {
	count := 0
	for searchEnd := len(data); searchEnd >= len(zipEndOfCentralDirectorySignature); {
		relative := bytes.LastIndex(data[:searchEnd], zipEndOfCentralDirectorySignature)
		if relative < 0 {
			break
		}
		searchEnd = relative
		if relative+22 > len(data) {
			continue
		}
		disk := binary.LittleEndian.Uint16(data[relative+4 : relative+6])
		centralDisk := binary.LittleEndian.Uint16(data[relative+6 : relative+8])
		entriesOnDisk := binary.LittleEndian.Uint16(data[relative+8 : relative+10])
		totalEntries := binary.LittleEndian.Uint16(data[relative+10 : relative+12])
		if disk != 0 || centralDisk != 0 || entriesOnDisk != totalEntries {
			continue
		}
		commentLength := int(binary.LittleEndian.Uint16(data[relative+20 : relative+22]))
		archiveEnd := relative + 22 + commentLength
		centralSize := int(binary.LittleEndian.Uint32(data[relative+12 : relative+16]))
		centralOffset := int(binary.LittleEndian.Uint32(data[relative+16 : relative+20]))
		archiveStart := relative - centralSize - centralOffset
		if archiveStart < 0 || archiveEnd > len(data) || archiveStart >= archiveEnd {
			continue
		}
		reader, err := zip.NewReader(bytes.NewReader(data[archiveStart:archiveEnd]), int64(archiveEnd-archiveStart))
		if err != nil {
			continue
		}
		entries := map[string]int{}
		for _, entry := range reader.File {
			entries[entry.Name]++
		}
		if entries["index.html"] == 1 && entries["main.js"] == 1 &&
			(entries["compiled_tailwind.css"] == 1 || entries["jetbox.css"] == 1) &&
			entries["compiled_tailwind.css"] <= 1 && entries["jetbox.css"] <= 1 {
			count++
		}
	}
	return count
}

// writeDarwinAgentLanguageServerUIFixture creates a structurally real test
// Language Server: the prefix is the current darwin Go test Mach-O and the
// suffix is one verified Agent UI ZIP.  Tests can therefore exercise the
// production Mach-O and archive gates without weakening either gate or
// checking a large proprietary binary into the repository.
func writeDarwinAgentLanguageServerUIFixture(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	machO, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	// A deliberately quick first-pass compression gives the production Zopfli
	// rebuild deterministic room for its injected UI code while keeping this
	// fixture small.  The official 2.6.0 archive naturally has the same margin.
	writer.RegisterCompressor(zip.Deflate, func(destination io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(destination, flate.BestSpeed)
	})
	entries := map[string]string{
		"index.html":            `<!doctype html><html><body><div id="root"></div></body></html>`,
		"main.js":               darwinAgentImageUIFixture() + strings.Repeat("/* deterministic Agent UI compression margin */", 8192),
		"compiled_tailwind.css": `.generated-image{max-width:100%;height:auto}`,
		"jetbox.css":            `.artifact-image{object-fit:contain}`,
	}
	for _, name := range []string{"index.html", "main.js", "compiled_tailwind.css", "jetbox.css"} {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte(entries[name])); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	data := make([]byte, 0, len(machO)+archive.Len())
	data = append(data, machO...)
	data = append(data, archive.Bytes()...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if recognized, err := validateDarwinAgentEmbeddedUISource(path); err != nil || !recognized {
		t.Fatalf("synthetic Darwin Agent Language Server was not production-valid: recognized=%t err=%v", recognized, err)
	}
}

// darwinAgentImageUIFixture is frozen from the verified Agent 2.0.6/2.0.10/
// 2.3.1/2.5.0/2.6.0 UI family.  It intentionally contains both the native
// generated-image card and artifact markdown renderer required by the two
// independent safety matchers.
func darwinAgentImageUIFixture() string {
	return `
const x={createElement:(type,props,...children)=>({type,props:{...(props||{}),children}}),useState:value=>[value,()=>{}],useCallback:value=>value,useContext:()=>({})},On={},rm=(...v)=>v.join(" "),IN=value=>value,Q4a=()=>({open:()=>{},modal:null}),R4a="button",T="icon",tV="card",vV="title",hE=status=>status==="loading",sK=()=>true,ty=()=>({stepHandler:{openFile:()=>{},resolveArtifactUrl:value=>value}}),JU=()=>({blobUrl:void 0}),TD=media=>media.data;
var S4a=({src:a,alt:b,originalFilePath:c,popout:e=!0,className:f="",openUri:g})=>{var [h,k]=(0,x.useState)(!1),l=IN(a),{open:m,modal:n}=Q4a(l,"image"),p=r=>{r&&r.stopPropagation();!h&&g&&c&&(r=c.includes("://")?c:c)&&g(r.toString())};if(!a||h)return x.createElement("span",{className:"text-sm"},"Preview unavailable");a=!(!g||!c);return x.createElement("div",{className:"group/media relative block w-full max-w-3xl"},x.createElement("div",{className:rm("relative overflow-hidden rounded-lg border",e?"cursor-pointer":"",f),onClick:e?m:void 0},x.createElement("img",{src:l,alt:b||"Artifact image",className:"w-full h-auto object-contain",onError:()=>{k(!0)}}),e&&x.createElement(R4a,{onOpen:m,onOpenInTab:a?p:void 0,className:"absolute top-2 right-2"})),b&&x.createElement("div",{className:"text-sm text-secondary-foreground text-center italic"},b),n)};
var mcb=({step:a,status:b})=>{var {stepHandler:{openFile:c,resolveArtifactUrl:e}={}}=ty(),{isRemoteControl:f}=(0,x.useContext)(On),g=a.generatedMedia,{blobUrl:h}=JU(f&&g?.uri?g.uri:void 0),k=void 0;f&&g?.uri?k=h:g?.uri?k=e?.(g.uri)||void 0:g?.payload.case==="inlineData"&&(k=g?TD(g):void 0);b=hE(b)&&!k;f=(0,x.useCallback)(()=>{g?.uri&&c?.(g.uri)},[g?.uri,c]);return k||b?x.createElement("div",{className:"px-2 py-1"},a.prompt&&x.createElement("div",{},a.prompt),b?x.createElement("div",{}):x.createElement("div",{onClick:f},x.createElement("img",{src:k,alt:"Generated image preview",className:"w-full h-auto rounded object-contain"}))):null};
const ncb={"gemini-3.1-flash-image":{displayName:"Gemini 3.1 Flash Image",isNewModel:!0}},tool={generateImage:{isRendered:sK(),icon:a=>x.createElement(T,{name:"image",...a}),isTool:!0,renderer:({step:a,status:b,error:c})=>{var e=!!a.generatedMedia?.uri,f=a.modelName?ncb[a.modelName]:void 0,g=f?.displayName||"Gemini";f=f?.isNewModel??!1;e=hE(b)?` + "`Generating with ${g} \\ud83c\\udf4c`" + `:e?` + "`Generated with ${g} \\ud83c\\udf4c`" + `:` + "`Generate with ${g} \\ud83c\\udf4c`" + `;return x.createElement(tV,{loading:hE(b),title:x.createElement(vV,{prefix:f?x.createElement("span",{},"New"):void 0,content:e}),supplementaryView:c?null:x.createElement(mcb,{step:a,status:b}),cta:null})}}};
`
}
