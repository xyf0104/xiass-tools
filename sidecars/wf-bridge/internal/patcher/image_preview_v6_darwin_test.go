//go:build darwin

package patcher

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func darwinImageGenerationDedupeV3Fixture(t testing.TB) string {
	t.Helper()
	patched, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture() + imageArtifactMarkdownRendererFixture())
	if !result.Changed || !darwinImageRendererReady([]byte(patched)) {
		t.Fatalf("baseline Darwin renderer did not produce v6: %#v", result)
	}
	currentRuntime := `globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageMountedDuplicateHiderDefinition() + generatedImageRememberDefinition()
	legacyRuntime := generatedImageV3MountedDuplicateHiderDefinition() + generatedImageV3RememberDefinition()
	legacy := strings.Replace(patched, currentRuntime, legacyRuntime, 1)
	legacy = strings.Replace(legacy, generatedImageConsumeDefinition(), "", 1)
	legacy = strings.Replace(legacy, imageGenerationDedupePatchMarker, imageGenerationDedupePatchV3Marker, 1)
	currentExpression := `$wfImageDuplicate=(` + generatedImageConsumerRuntime() + `!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4(e,u,r)),`
	legacyExpression := `$wfImageDuplicate=!!globalThis.__antigravityWFIsRecentGeneratedImageV2&&[e,u,r].some(value=>globalThis.__antigravityWFIsRecentGeneratedImageV2(value)),`
	legacy = strings.Replace(legacy, currentExpression, legacyExpression, 1)
	legacy = strings.Replace(legacy, imageArtifactThumbnailStyle, "", 1)
	return legacy
}

func darwinImageGenerationDedupeV4Fixture(t testing.TB) string {
	t.Helper()
	patched, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture() + imageArtifactMarkdownRendererFixture())
	if !result.Changed || !darwinImageRendererReady([]byte(patched)) {
		t.Fatalf("baseline Darwin renderer did not produce v6: %#v", result)
	}
	currentRuntime := `globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageMountedDuplicateHiderDefinition() + generatedImageRememberDefinition() +
		generatedImageIsRecentDefinition() + generatedImageConsumeDefinition()
	legacyRuntime := `globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageMountedDuplicateHiderDefinition() + generatedImageV4RememberDefinition() +
		generatedImageIsRecentDefinition() + generatedImageV4ConsumeDefinition()
	legacy := strings.Replace(patched, currentRuntime, legacyRuntime, 1)
	currentExpression := `$wfImageDuplicate=(` + generatedImageConsumerRuntime() + `!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4(e,u,r)),`
	legacyExpression := `$wfImageDuplicate=!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4(e,u,r),`
	legacy = strings.Replace(legacy, currentExpression, legacyExpression, 1)
	legacy = strings.Replace(legacy, imageArtifactThumbnailStyle, "", 1)
	legacy = strings.Replace(legacy, imageGenerationDedupePatchMarker, imageGenerationDedupePatchV4Marker, 1)
	return legacy
}

func darwinImageGenerationDedupeV5Fixture(t testing.TB) string {
	t.Helper()
	patched, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture() + ";" + imageGenerationUIRendererFixture() + imageArtifactMarkdownRendererFixture())
	if !result.Changed || !darwinImageRendererReady([]byte(patched)) {
		t.Fatalf("baseline Darwin renderer did not produce v6: %#v", result)
	}
	legacy := strings.Replace(patched, imageGenerationDedupePatchMarker, imageGenerationDedupePatchV5Marker, 1)
	return strings.Replace(legacy, imageArtifactThumbnailStyle, "", 1)
}

func TestDarwinMigratesAllIDEImageDedupeRevisionsToV6(t *testing.T) {
	tests := []struct {
		name   string
		legacy func(testing.TB) string
	}{
		{name: "v3", legacy: darwinImageGenerationDedupeV3Fixture},
		{name: "v4", legacy: darwinImageGenerationDedupeV4Fixture},
		{name: "v5", legacy: darwinImageGenerationDedupeV5Fixture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			legacy := test.legacy(t)
			if darwinImageRendererReady([]byte(legacy)) {
				t.Fatalf("legacy Darwin %s renderer was incorrectly ready", test.name)
			}
			updated, result := patchImagePreviewRenderer(legacy)
			if !result.Recognized || !result.Changed || !darwinImageRendererReady([]byte(updated)) {
				t.Fatalf("Darwin %s renderer was not migrated to v6: %#v", test.name, result)
			}
			for _, required := range []string{imageGenerationDedupePatchMarker, imageArtifactThumbnailStyle, `antigravity-wf-generated-image-events-v5`, generatedImageConsumerRuntime()} {
				if !strings.Contains(updated, required) {
					t.Fatalf("Darwin %s migration is missing %q", test.name, required)
				}
			}
			if strings.Count(updated, imageArtifactThumbnailStyle) != 1 || !strings.Contains(updated, `prompt:"keep this prompt"`) {
				t.Fatalf("Darwin %s migration changed the Prompt card or styled more than the artifact image", test.name)
			}
			second, secondResult := patchImagePreviewRenderer(updated)
			if !secondResult.Recognized || secondResult.Changed || second != updated {
				t.Fatalf("Darwin %s migration is not idempotent: %#v", test.name, secondResult)
			}
		})
	}
}

func TestDarwinGeneratedImageDedupeSharesSingleUseEventAcrossRendererGlobals(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; Darwin cross-renderer dedupe check skipped")
	}
	path := filepath.Join(t.TempDir(), "darwin-generated-image-cross-global-dedupe.js")
	source := `"use strict";
const vm=require("vm"),values=new Map;
const localStorage={getItem:key=>values.has(key)?values.get(key):null,setItem:(key,value)=>values.set(key,String(value))};
const card=vm.createContext({localStorage});
vm.runInContext(` + strconv.Quote(generatedImageRegistrationRuntime(`"file:///Users/Test/generated-card.png"`)) + `,card);
let queue=JSON.parse(values.get("antigravity-wf-generated-image-events-v5"));
queue[0].time=Date.now()-300000;
values.set("antigravity-wf-generated-image-events-v5",JSON.stringify(queue));
const artifact=vm.createContext({localStorage});
const first=vm.runInContext(` + strconv.Quote(generatedImageConsumerRuntime()+`globalThis.__antigravityWFConsumeGeneratedImageV4("file:///Users/Test/artifacts/renamed.png")`) + `,artifact);
const second=vm.runInContext(` + strconv.Quote(`globalThis.__antigravityWFConsumeGeneratedImageV4("file:///Users/Test/normal.png")`) + `,artifact);
process.stdout.write(JSON.stringify({first,second,stored:JSON.parse(values.get("antigravity-wf-generated-image-events-v5"))}));`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("Darwin cross-renderer dedupe failed node --check: %s: %v", output, err)
	}
	output, err := exec.Command(node, path).Output()
	if err != nil {
		t.Fatalf("Darwin cross-renderer dedupe failed at runtime: %v", err)
	}
	var got struct {
		First  bool `json:"first"`
		Second bool `json:"second"`
		Stored []struct {
			Consumed bool `json:"consumed"`
		} `json:"stored"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("Darwin cross-renderer dedupe returned invalid JSON %q: %v", output, err)
	}
	if !got.First || got.Second || len(got.Stored) != 1 || !got.Stored[0].Consumed {
		t.Fatalf("Darwin cross-renderer event was not single-use after five minutes: %#v", got)
	}
}

func darwinNativePreviewRendererFixture() string {
	return `;const ctx=()=>({stepHandler:{openFile:()=>{},resolveArtifactUrl:value=>value}}),remote=()=>({isRemoteControl:false}),remoteBlob=()=>({blobUrl:void 0}),inlineData=value=>value,loading=()=>false;let NativePreview;NativePreview=({step:e,status:t})=>{let{stepHandler:{openFile:r,resolveArtifactUrl:n}={}}=ctx(),{isRemoteControl:a}=remote(),i=e.generatedMedia,{blobUrl:s}=remoteBlob(a&&i?.uri?i.uri:void 0),o;a&&i?.uri?o=s:i?.uri?o=n?.(i.uri)||void 0:i?.payload.case==="inlineData"&&(o=i?inlineData(i):void 0);let l=loading(t)&&!o,d=()=>{i?.uri&&r?.(i.uri)};return m("div",{children:[e.prompt&&m("div",{children:m("div",{children:"Prompt"})}),l?null:m("img",{src:o,alt:"Generated image preview",onClick:d})]})};`
}

func darwinArtifactMarkdownIfRendererFixture() string {
	return `;const ke=initial=>[initial,()=>{}],bun=value=>value,F=(component,props)=>({component,props}),modal=()=>({open:()=>{},modal:null});let _Ci;_Ci=({src:e,alt:t,originalFilePath:r,popout:n=!0,className:a="",openUri:i})=>{let[s,o]=ke(!1),u=bun(e),{open:d,modal:h}=modal(u),m=()=>{};if(!e||s)return F("fallback",{});let p=!!(i&&r);return F("div",{src:u,alt:t||"Artifact image",originalFilePath:r,popout:n,className:a,openUri:i,open:d,modal:h,canOpen:p,onError:()=>o(!0),noop:m})};`
}

func TestDarwinIDE255NativePreviewGetsV6WithoutLegacyFallback(t *testing.T) {
	source := imageGenerationCombinedUIRendererFixture() + darwinNativePreviewRendererFixture() + darwinArtifactMarkdownIfRendererFixture()
	updated, result := patchImagePreviewRenderer(source)
	if !result.Recognized || !result.Changed || !darwinImageRendererReady([]byte(updated)) {
		t.Fatalf("Darwin native IDE renderer was not upgraded: %#v", result)
	}
	for _, required := range []string{imagePreviewNativeCompatibleMarker, imageGenerationUIPatchMarker, imageGenerationDedupePatchMarker, imageArtifactThumbnailStyle} {
		if !strings.Contains(updated, required) {
			t.Fatalf("Darwin native IDE renderer is missing %q", required)
		}
	}
	if strings.Contains(updated, imagePreviewPatchMarker) || strings.Count(updated, imageArtifactThumbnailStyle) != 1 {
		t.Fatal("Darwin native Prompt preview received the legacy fallback or duplicate thumbnail style")
	}
}
