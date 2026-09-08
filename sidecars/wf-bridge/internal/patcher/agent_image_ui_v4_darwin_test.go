//go:build darwin

package patcher

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Agent 2.8.1 uses this same native generated-image/artifact renderer family
// as the Windows fixture. This test verifies the shared renderer migration and
// runtime behavior on Darwin; Mach-O and embedded-ZIP gates are covered by the
// production-plan tests in agent_image_ui_patch_darwin_test.go.
func TestDarwinAgent281RendererFamilyGetsV4(t *testing.T) {
	updated, result := patchAgentImageUI(darwinAgentImageUIFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("Darwin Agent image UI fixture was not upgraded: %+v", result)
	}
	for _, marker := range []string{agentImageGenerationUIPatchMarker, agentImageGenerationDedupePatchMarker, imageGenerationUIPatchMarker, imageArtifactThumbnailStyle} {
		if !strings.Contains(updated, marker) {
			t.Fatalf("Darwin Agent image UI fixture is missing %s", marker)
		}
	}
	second, secondResult := patchAgentImageUI(updated)
	if !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("Darwin Agent v4 image UI patch is not idempotent: %+v", secondResult)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; Darwin Agent image UI runtime check skipped")
	}
	path := filepath.Join(t.TempDir(), "darwin-agent-image-ui-v4.js")
	script := `"use strict";` + updated + `
let wfNow=Date.now();Date.now=()=>wfNow;
const title=(modelName,status="done",hasMedia=true)=>tool.generateImage.renderer({step:{modelName,generatedMedia:hasMedia?{uri:"file:///Users/Test/generated.png"}:void 0},status}).props.title.props.content;
mcb({step:{generatedMedia:{uri:"file:///Users/Test/generated.png"}},status:"done"});
wfNow+=300000;
const matching=S4a({src:"vscode-file://vscode-app/Users/Test/generated.png",alt:"duplicate",originalFilePath:"/Users/Test/generated.png"});
const different=S4a({src:"file:///Users/Test/normal.png",alt:"normal",originalFilePath:"/Users/Test/normal.png"});
$wfRememberGeneratedImageURI("file:///Users/Test/generated-two.png");
const renamed=S4a({src:"file:///Users/Test/artifacts/saved-under-a-different-name.png",alt:"renamed",originalFilePath:"/Users/Test/artifacts/saved-under-a-different-name.png"});
const afterRenamed=S4a({src:"file:///Users/Test/normal-after.png",alt:"normal after",originalFilePath:"/Users/Test/normal-after.png"});
process.stdout.write(JSON.stringify({gpt:title("gpt-image-2"),gemini:title("gemini-3.1-flash-image"),unknown:title("image-alpha"),loading:title(void 0,"loading",false),matching,differentAlt:different?.props?.children?.[1]?.props?.children?.[0],renamed,afterRenamedAlt:afterRenamed?.props?.children?.[1]?.props?.children?.[0]}));`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("Darwin Agent v4 image UI failed node --check: %s: %v", output, err)
	}
	output, err := exec.Command(node, path).Output()
	if err != nil {
		t.Fatalf("Darwin Agent v4 image UI runtime failed: %v", err)
	}
	var got struct {
		GPT          string `json:"gpt"`
		Gemini       string `json:"gemini"`
		Unknown      string `json:"unknown"`
		Loading      string `json:"loading"`
		Matching     any    `json:"matching"`
		DifferentAlt string `json:"differentAlt"`
		Renamed      any    `json:"renamed"`
		AfterRenamed string `json:"afterRenamedAlt"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("Darwin Agent v4 image UI returned invalid JSON %q: %v", output, err)
	}
	if got.GPT != "Generated with GPT Image 2" || got.Gemini != "Generated with Gemini 3.1 Flash Image \U0001F34C" ||
		got.Unknown != "Generated with image-alpha" || got.Loading != "Generating image" || got.Matching != nil ||
		got.DifferentAlt != "normal" || got.Renamed != nil || got.AfterRenamed != "normal after" {
		t.Fatalf("unexpected Darwin Agent v4 image UI state: %+v", got)
	}
}

func TestDarwinPatchAgentImageUIMigratesAllManagedDedupeRevisions(t *testing.T) {
	current, result := patchAgentImageUI(darwinAgentImageUIFixture())
	if !result.Changed {
		t.Fatalf("failed to construct current Darwin Agent fixture: %+v", result)
	}
	tests := []struct {
		name       string
		marker     string
		oldRuntime string
	}{
		{name: "v1", marker: agentImageGenerationDedupePatchV1Marker, oldRuntime: agentGeneratedImageDedupeV1Registry()},
		{name: "v2", marker: agentImageGenerationDedupePatchV2Marker, oldRuntime: agentGeneratedImageDedupeV2Registry()},
		{name: "v3", marker: agentImageGenerationDedupePatchV3Marker},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := strings.Replace(current, agentImageGenerationDedupePatchMarker, test.marker, 1)
			if test.oldRuntime != "" {
				legacy = strings.Replace(legacy, agentGeneratedImageDedupeRegistry(), test.oldRuntime, 1)
			}
			legacy = strings.Replace(legacy, imageArtifactThumbnailStyle, "", 1)
			updated, migration := patchAgentImageUI(legacy)
			if !migration.Recognized || !migration.Changed || !strings.Contains(updated, agentImageGenerationDedupePatchMarker) ||
				strings.Contains(updated, test.marker) || !strings.Contains(updated, imageArtifactThumbnailStyle) ||
				!strings.Contains(updated, `now-$wfGeneratedImageEvents[index].time>6E5`) {
				t.Fatalf("Darwin Agent %s dedupe was not migrated to v4: %+v", test.name, migration)
			}
			second, secondResult := patchAgentImageUI(updated)
			if !secondResult.Recognized || secondResult.Changed || second != updated {
				t.Fatalf("migrated Darwin Agent %s dedupe is not idempotent: %+v", test.name, secondResult)
			}
		})
	}
}
