package patcher

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func imagePreviewOriginalRendererFixture() string {
	return `prefix;a=e.generatedMedia,i;a?.uri?i=n?.(a.uri)||void 0:a?.payload.case==="inlineData"&&(i=a?YI(a):void 0);let s=Ia(t)&&!i;suffix`
}

func imagePreviewV2RendererFixture() string {
	return `prefix;/*antigravity-wf:image-preview-fallback:v2*/a=e.generatedMedia||e.generatedImage,i;a?.uri?(i=n?.(a.uri),i=(i&&typeof i.getState==="function"?i.getState():i||void 0)):a?.payload?.case==="inlineData"&&(i=a?YI(a):void 0),!i&&a?.base64Data&&(i="data:"+(a.mimeType||"image/png")+";base64,"+(typeof a.base64Data==="string"?a.base64Data:btoa(Array.from(a.base64Data).map(i=>String.fromCharCode(i)).join(""))));let s=Ia(t)&&!i;suffix`
}

// imagePreviewV3RendererFixture is the compact v3 shape observed in a real
// Antigravity 1.23.2 bundle. It deliberately retains the old file URI logic
// so migration tests prove installed users receive the drive-letter fix.
func imagePreviewV3RendererFixture() string {
	return `prefix;/*antigravity-wf:image-preview-fallback:v3*/a=e.generatedMedia||e.generatedImage,i;a?.uri?(i=n?.(a.uri),i=(i&&typeof i.getState==="function"?i.getState():i||void 0)):a?.payload?.case==="inlineData"&&(i=a?YI(a):void 0),!i&&a?.base64Data&&(i="data:"+(a.mimeType||"image/png")+";base64,"+(typeof a.base64Data==="string"?a.base64Data:btoa(Array.from(a.base64Data).map(i=>String.fromCharCode(i)).join("")))),!i&&a?.uri&&typeof a.uri==="string"&&a.uri.startsWith("file://")&&(i=decodeURIComponent(a.uri.replace(/^file:\/\//,"")));let s=Ia(t)&&!i;suffix`
}

// imagePreviewV4RendererFixture is the v4 expression shipped in v1.4.20.
// Its resolver branch retains any truthy value, including Promise.resolve(),
// which React later stringifies into an unusable image source.
func imagePreviewV4RendererFixture() string {
	return `prefix;/*antigravity-wf:image-preview-fallback:v4*/a=e.generatedMedia||e.generatedImage,i;a?.uri?(i=n?.(a.uri),i=(i&&typeof i.getState==="function"?i.getState():i||void 0)):a?.payload?.case==="inlineData"&&(i=a?YI(a):void 0),!i&&a?.base64Data&&(i="data:"+(a.mimeType||"image/png")+";base64,"+(typeof a.base64Data==="string"?a.base64Data:btoa(Array.from(a.base64Data).map(i=>String.fromCharCode(i)).join("")))),!i&&a?.uri&&typeof a.uri==="string"&&a.uri.startsWith("file://")&&(i=((u)=>{let p=u.replace(/^file:\/\/\/([A-Za-z]:\/)/,"$1");p===u&&(p=u.startsWith("file:///")?u.slice(7):u.replace(/^file:\/\//,"//"));try{return decodeURIComponent(p)}catch{return p}})(a.uri));let s=Ia(t)&&!i;suffix`
}

func imagePreviewV5RendererFixture() string {
	fixture := strings.Replace(imagePreviewV4RendererFixture(), imagePreviewPatchV4Marker, imagePreviewPatchV5Marker, 1)
	return strings.Replace(fixture,
		`i=(i&&typeof i.getState==="function"?i.getState():i||void 0)`,
		`i=(i&&typeof i.getState==="function"?i.getState():i),i=typeof i==="string"?i:void 0`,
		1,
	)
}

func imageGenerationUIRendererFixture() string {
	return `const bt=initial=>[initial,()=>{}],Le=callback=>callback,Ga=status=>status==="loading",F=(component,props)=>({component,props}),io=()=>{},Lma=()=>{};let Oma,Yma,Pma,rBe,Mma;Oma={"gemini-3.1-flash-image":{displayName:"Gemini 3.1 Flash Image",isNewModel:!0},"gemini-3-pro-image":{displayName:"Gemini 3 Pro Image",isNewModel:!1},"gemini-2.5-flash-image":{displayName:"Gemini 2.5 Flash Image",isNewModel:!1}},Yma=({step:e,status:t})=>{let r=!!e.generatedImage?.uri,n=e.modelName?Oma[e.modelName]:void 0,a=n?.displayName||"Gemini",i=n?.isNewModel??!1;return F("div",{className:"flex items-center gap-1",children:[i&&F("span",{className:"text-xs bg-gray-500/20 rounded px-1 py-px",children:"New"}),F("span",{children:Ga(t)?` + "`Generating with ${a} \\u{1F34C}`" + `:r?` + "`Generated with ${a} \\u{1F34C}`" + `:` + "`Generate with ${a} \\u{1F34C}`" + `})]})},Pma=({step:e,status:t,error:r})=>F(io,{loading:Ga(t),title:F(Yma,{step:e,status:t}),supplementaryView:r?null:F(Lma,{step:e,status:t}),cta:null}),rBe=({renderInfo:e,fallback:t})=>null,Mma=({args:e,status:t,loading:r})=>{let{renderers:n}={},a=n?.markdown,[i,s]=bt(!1),o=Le(()=>{s(b=>!b)},[]);return F(io,{loading:r,status:t,title:null,supplementaryView:null,cta:null,isExpanded:i,onToggle:o,hasSupplementaryView:!1})};`
}

// imageGenerationUIWithMediaRendererFixture is the title component emitted by
// the real macOS Antigravity 1.23.2 renderer. It must remain separate from the
// simpler generatedImage fixture above: matching its four media aliases as if
// they were interchangeable would risk changing unrelated future code.
func imageGenerationUIWithMediaRendererFixture() string {
	return strings.Replace(
		imageGenerationUIRendererFixture(),
		`let r=!!e.generatedImage?.uri,`,
		`let r=!!(e.generatedMedia?.uri||e.generatedMedia?.payload?.value?.length||e.generatedImage?.uri||e.generatedImage?.base64Data),`,
		1,
	)
}

// imageGenerationCombinedUIRendererFixture mirrors the IDE 2.1.x renderer
// where title and result container live in the same component.
func imageGenerationCombinedUIRendererFixture() string {
	return `const Za=status=>status==="loading",m=(component,props)=>({component,props}),co=()=>{},es=()=>{},UEi=()=>{};let GEi,XEi;GEi={"gemini-3.1-flash-image":{displayName:"Gemini 3.1 Flash Image",isNewModel:!0}},XEi=({step:e,status:t,error:r})=>{let n=!!e.generatedMedia?.uri,a=e.modelName?GEi[e.modelName]:void 0,i=a?.displayName||"Gemini",s=a?.isNewModel??!1,o=Za(t)?` + "`Generating with ${i} \\u{1F34C}`" + `:n?` + "`Generated with ${i} \\u{1F34C}`" + `:` + "`Generate with ${i} \\u{1F34C}`" + `;return m(co,{loading:Za(t),title:m(es,{prefix:s?m("span",{children:"New"}):void 0,content:o}),supplementaryView:r?null:m(UEi,{step:e,status:t}),cta:null})};`
}

func imageGenerationCombinedUIWithMediaRendererFixture() string {
	return strings.Replace(
		imageGenerationCombinedUIRendererFixture(),
		`let n=!!e.generatedMedia?.uri,`,
		`let n=!!(e.generatedMedia?.uri||e.generatedMedia?.payload?.value?.length||e.generatedImage?.uri||e.generatedImage?.base64Data),`,
		1,
	)
}

// imageArtifactMarkdownRendererFixture mirrors the dedicated Markdown image
// component used by IDE 2.1. It intentionally retains a prompt-card value so
// the dedupe test can prove that only the duplicate body image is hidden.
func imageArtifactMarkdownRendererFixture() string {
	return `;const ke=initial=>[initial,()=>{}],bun=value=>value,F=(component,props)=>({component,props});const wfPromptCard={prompt:"keep this prompt",src:"file:///C:/Users/Test/Image.png"};let _Ci;_Ci=({src:e,alt:t,originalFilePath:r,popout:n=!0,className:a="",openUri:i})=>{let[s,o]=ke(!1),u=bun(e),l=0;return!e||s?F("fallback",{}):F("img",{src:u,alt:t||"Artifact image",originalFilePath:r,popout:n,className:a,openUri:i,l})};`
}

func TestPatchImagePreviewRendererAddsV7Fallback(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("unmodified 1.23.2 renderer was not patched: %#v", result)
	}
	for _, required := range []string{
		imagePreviewPatchMarker,
		`a=e.generatedMedia||e.generatedImage`,
		`typeof i.getState==="function"`,
		`.base64Data`,
		`startsWith("file://")`,
		`e.generatedImage&&e.generatedImage!==a`,
		`typeof i==="string"`,
		`vscode-file://vscode-app`,
		`encodeURI(v)`,
	} {
		if !strings.Contains(updated, required) {
			t.Fatalf("v7 renderer is missing %q: %s", required, updated)
		}
	}
	second, secondResult := patchImagePreviewRenderer(updated)
	if !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("v7 renderer must be idempotent: result=%#v", secondResult)
	}
	assertImagePreviewJavaScriptSyntax(t, updated)
}

func TestPatchImageGenerationUIRendererUsesActualModelAndStartsExpanded(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imageGenerationUIRendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("known image-generation UI renderer was not patched: %#v", result)
	}
	for _, required := range []string{
		imageGenerationUIPatchMarker,
		`/^gpt-image-(\d+)$/i.exec(modelName||"")`,
		`isExpanded:$wfImageExpanded`,
		`onToggle:$wfToggleImageExpanded`,
		`hasSupplementaryView:!r`,
		`${$wfIsGeminiImage?" \u{1F34C}":""}`,
		"`Generating image`",
		`/^gemini[-_](.+)$/i`,
	} {
		if !strings.Contains(updated, required) {
			t.Fatalf("patched image-generation UI is missing %q: %s", required, updated)
		}
	}
	second, secondResult := patchImagePreviewRenderer(updated)
	if !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("patched image-generation UI must be idempotent: result=%#v", secondResult)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; image-generation UI runtime check skipped")
	}
	path := filepath.Join(t.TempDir(), "image-generation-ui.js")
	source := updated + `
const gptTitle=Yma({step:{modelName:"gpt-image-2"},status:"done"}).props.children[1].props.children;
const geminiTitle=Yma({step:{modelName:"gemini-3.1-flash-image"},status:"done"}).props.children[1].props.children;
const genericGeminiTitle=Yma({step:{modelName:"gemini-3.6-flash"},status:"done"}).props.children[1].props.children;
const unknownTitle=Yma({step:{modelName:"image-alpha"},status:"done"}).props.children[1].props.children;
const loadingTitle=Yma({step:{},status:"loading"}).props.children[1].props.children;
const panel=Pma({step:{},status:"done"}).props;
process.stdout.write(JSON.stringify({gptTitle,geminiTitle,genericGeminiTitle,unknownTitle,loadingTitle,expanded:panel.isExpanded,hasToggle:typeof panel.onToggle==="function",hasSupplementaryView:panel.hasSupplementaryView}));`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("patched image-generation UI failed node --check: %s: %v", output, err)
	}
	output, err := exec.Command(node, path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		GPTTitle             string `json:"gptTitle"`
		GeminiTitle          string `json:"geminiTitle"`
		GenericGeminiTitle   string `json:"genericGeminiTitle"`
		UnknownTitle         string `json:"unknownTitle"`
		LoadingTitle         string `json:"loadingTitle"`
		Expanded             bool   `json:"expanded"`
		HasToggle            bool   `json:"hasToggle"`
		HasSupplementaryView bool   `json:"hasSupplementaryView"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("patched image-generation UI did not return JSON %q: %v", output, err)
	}
	if got.GPTTitle != "Generate with GPT Image 2" || got.GeminiTitle != "Generate with Gemini 3.1 Flash Image \U0001F34C" || got.GenericGeminiTitle != "Generate with Gemini 3.6 Flash \U0001F34C" || got.UnknownTitle != "Generate with image-alpha" || got.LoadingTitle != "Generating image" || !got.Expanded || !got.HasToggle || !got.HasSupplementaryView {
		t.Fatalf("unexpected patched image-generation UI state: %#v", got)
	}
}

func TestPatchImageGenerationUIWithMediaRendererUsesActualModelAndStartsExpanded(t *testing.T) {
	original := imageGenerationUIWithMediaRendererFixture()
	titleMatches := findImageGenerationTitleRendererMatches(original)
	if len(titleMatches) != 1 {
		t.Fatalf("expected exactly one strict macOS title match, got %#v", titleMatches)
	}
	if got := titleMatches[0]; got.component != "Yma" || got.step != "e" || got.status != "t" || got.resolvedModel != "n" || got.displayName != "a" || got.isNewModel != "i" {
		t.Fatalf("unexpected macOS title match metadata: %#v", got)
	}

	updated, result := patchImagePreviewRenderer(original)
	if !result.Recognized || !result.Changed || !strings.Contains(updated, imageGenerationUIPatchMarker) {
		t.Fatalf("known macOS image-generation UI renderer was not patched: %#v", result)
	}
	second, secondResult := patchImagePreviewRenderer(updated)
	if !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("patched macOS image-generation UI must be idempotent: result=%#v", secondResult)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; macOS image-generation UI runtime check skipped")
	}
	path := filepath.Join(t.TempDir(), "image-generation-ui-macos.js")
	source := updated + `
const generatedMediaTitle=Yma({step:{modelName:"gpt-image-2",generatedMedia:{payload:{value:"image-bytes"}}},status:"done"}).props.children[1].props.children;
const generatedImageTitle=Yma({step:{modelName:"gpt-image-2",generatedImage:{base64Data:"image-bytes"}},status:"done"}).props.children[1].props.children;
const genericGeminiTitle=Yma({step:{modelName:"gemini-3.6-flash"},status:"done"}).props.children[1].props.children;
const loadingTitle=Yma({step:{},status:"loading"}).props.children[1].props.children;
const panel=Pma({step:{},status:"done"}).props;
process.stdout.write(JSON.stringify({generatedMediaTitle,generatedImageTitle,genericGeminiTitle,loadingTitle,expanded:panel.isExpanded,hasToggle:typeof panel.onToggle==="function",hasSupplementaryView:panel.hasSupplementaryView}));`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("patched macOS image-generation UI failed node --check: %s: %v", output, err)
	}
	output, err := exec.Command(node, path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		GeneratedMediaTitle  string `json:"generatedMediaTitle"`
		GeneratedImageTitle  string `json:"generatedImageTitle"`
		GenericGeminiTitle   string `json:"genericGeminiTitle"`
		LoadingTitle         string `json:"loadingTitle"`
		Expanded             bool   `json:"expanded"`
		HasToggle            bool   `json:"hasToggle"`
		HasSupplementaryView bool   `json:"hasSupplementaryView"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("patched macOS image-generation UI did not return JSON %q: %v", output, err)
	}
	if got.GeneratedMediaTitle != "Generated with GPT Image 2" || got.GeneratedImageTitle != "Generated with GPT Image 2" || got.GenericGeminiTitle != "Generate with Gemini 3.6 Flash \U0001F34C" || got.LoadingTitle != "Generating image" || !got.Expanded || !got.HasToggle || !got.HasSupplementaryView {
		t.Fatalf("unexpected patched macOS image-generation UI state: %#v", got)
	}
}

func TestPatchCombinedImageGenerationUIRendererPreservesMultiSourceModelTitles(t *testing.T) {
	for _, test := range []struct {
		name      string
		fixture   string
		step      string
		wantTitle string
	}{
		{
			name:      "IDE 2.1 generatedMedia URI layout",
			fixture:   imageGenerationCombinedUIRendererFixture(),
			step:      `{modelName:"gpt-image-2",generatedMedia:{uri:"image.png"}}`,
			wantTitle: "Generated with GPT Image 2",
		},
		{
			name:      "macOS generatedImage base64 layout",
			fixture:   imageGenerationCombinedUIWithMediaRendererFixture(),
			step:      `{modelName:"gpt-image-2",generatedImage:{base64Data:"image-bytes"}}`,
			wantTitle: "Generated with GPT Image 2",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, result := patchImagePreviewRenderer(test.fixture)
			if !result.Recognized || !result.Changed {
				t.Fatalf("known IDE 2.1 combined image renderer was not patched: %#v", result)
			}
			for _, required := range []string{
				imageGenerationUIPatchMarker,
				`/^gpt-image-(\d+)$/i.exec(modelName||"")`,
				`${$wfIsGeminiImage?" \u{1F34C}":""}`,
				"`Generating image`",
				`/^gemini[-_](.+)$/i`,
			} {
				if !strings.Contains(updated, required) {
					t.Fatalf("patched combined image-generation UI is missing %q: %s", required, updated)
				}
			}
			if second, secondResult := patchImagePreviewRenderer(updated); !secondResult.Recognized || secondResult.Changed || second != updated {
				t.Fatalf("patched combined image-generation UI must be idempotent: result=%#v", secondResult)
			}

			node, err := exec.LookPath("node")
			if err != nil {
				t.Skip("node is unavailable; combined image-generation UI runtime check skipped")
			}
			path := filepath.Join(t.TempDir(), "combined-image-generation-ui.js")
			source := updated + `
const title=(step,status="done")=>XEi({step,status}).props.title.props.content;
process.stdout.write(JSON.stringify({
  requested:title(` + test.step + `),
  gemini:title({modelName:"gemini-3.1-flash-image",generatedMedia:{uri:"image.png"}}),
  genericGemini:title({modelName:"gemini-3.6-flash",generatedMedia:{uri:"image.png"}}),
  loading:title({generatedMedia:void 0},"loading")
}));`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("patched combined image-generation UI failed node --check: %s: %v", output, err)
			}
			output, err := exec.Command(node, path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Requested     string `json:"requested"`
				Gemini        string `json:"gemini"`
				GenericGemini string `json:"genericGemini"`
				Loading       string `json:"loading"`
			}
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("patched combined image-generation UI did not return JSON %q: %v", output, err)
			}
			if got.Requested != test.wantTitle || got.Gemini != "Generated with Gemini 3.1 Flash Image \U0001F34C" || got.GenericGemini != "Generated with Gemini 3.6 Flash \U0001F34C" || got.Loading != "Generating image" {
				t.Fatalf("unexpected patched combined image-generation UI state: %#v", got)
			}
		})
	}
}

func TestPatchCombinedImageGenerationUIRendererRejectsMixedAliases(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace string
		with    string
	}{
		{
			name:    "generated image source belongs to another step",
			replace: `e.generatedImage?.base64Data`,
			with:    `other.generatedImage?.base64Data`,
		},
		{
			name:    "display name belongs to another resolved model",
			replace: `i=a?.displayName`,
			with:    `i=other?.displayName`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := strings.Replace(imageGenerationCombinedUIWithMediaRendererFixture(), test.replace, test.with, 1)
			updated, result := patchImagePreviewRenderer(original)
			if result.Recognized || result.Changed || updated != original {
				t.Fatalf("mixed IDE 2.1 aliases must remain untouched: result=%#v", result)
			}
		})
	}
}

func TestPatchDuplicateGeneratedImageRendererKeepsPromptAndExpiresAfterTenMinutes(t *testing.T) {
	original := imagePreviewOriginalRendererFixture() + imageArtifactMarkdownRendererFixture()
	updated, result := patchImagePreviewRenderer(original)
	if !result.Recognized || !result.Changed {
		t.Fatalf("known generated-image and artifact renderers were not patched: %#v", result)
	}
	for _, required := range []string{
		imagePreviewPatchMarker,
		imageGenerationDedupePatchMarker,
		`__antigravityWFGeneratedImageTimesV2`,
		`__antigravityWFIsRecentGeneratedImageV2`,
		`$wfImageDuplicate?null`,
		`now-seen<600000`,
		`prompt:"keep this prompt"`,
	} {
		if !strings.Contains(updated, required) {
			t.Fatalf("generated-image dedupe patch is missing %q: %s", required, updated)
		}
	}
	if second, secondResult := patchImagePreviewRenderer(updated); !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("generated-image dedupe patch must be idempotent: result=%#v", secondResult)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; generated-image dedupe runtime check skipped")
	}
	path := filepath.Join(t.TempDir(), "generated-image-dedupe.js")
	source := `"use strict";Date.now=()=>1000;const prefix=0,suffix=0,e={generatedMedia:{uri:"file:///C:/Users/Test/Image.png"}},n=()=>void 0,YI=()=>void 0,Ia=()=>!1,t={};let a,i;` + updated + `
const matching=_Ci({src:"vscode-file://vscode-app/C:/Users/Test/Image.png",alt:"Artifact image",originalFilePath:"C:\\\\Users\\\\Test\\\\Image.png"});
const different=_Ci({src:"file:///C:/Users/Test/Other.png",alt:"Normal Markdown image",originalFilePath:"C:\\\\Users\\\\Test\\\\Other.png"});
const caseVariant=_Ci({src:"file:///C:/Users/Test/image.png",alt:"Case-sensitive image",originalFilePath:"C:\\\\Users\\\\Test\\\\image.png"});
Date.now=()=>601000;
const expired=_Ci({src:"vscode-file://vscode-app/C:/Users/Test/Image.png",alt:"Expired image",originalFilePath:"C:\\\\Users\\\\Test\\\\Image.png"});
process.stdout.write(JSON.stringify({matching,different,differentAlt:different?.props?.alt,caseVariant,caseVariantAlt:caseVariant?.props?.alt,expired,expiredAlt:expired?.props?.alt,prompt:wfPromptCard.prompt}));`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("generated-image dedupe failed node --check: %s: %v", output, err)
	}
	// Node 22+ may emit a localStorage CLI warning on stderr even though the
	// renderer produced valid JSON on stdout. Keep the business payload
	// separate so the warning cannot corrupt the JSON assertion below.
	output, err := exec.Command(node, path).Output()
	if err != nil {
		t.Fatalf("generated-image dedupe failed at runtime: %s: %v", output, err)
	}
	var got struct {
		Matching       any    `json:"matching"`
		Different      any    `json:"different"`
		DifferentAlt   string `json:"differentAlt"`
		CaseVariant    any    `json:"caseVariant"`
		CaseVariantAlt string `json:"caseVariantAlt"`
		Expired        any    `json:"expired"`
		ExpiredAlt     string `json:"expiredAlt"`
		Prompt         string `json:"prompt"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("generated-image dedupe returned invalid JSON %q: %v", output, err)
	}
	if got.Matching != nil {
		t.Fatalf("matching generated artifact was not hidden: %#v", got.Matching)
	}
	if got.Different == nil || got.DifferentAlt != "Normal Markdown image" || got.CaseVariant == nil || got.CaseVariantAlt != "Case-sensitive image" {
		t.Fatalf("different Markdown image was incorrectly hidden or changed: %#v", got)
	}
	if got.Expired == nil || got.ExpiredAlt != "Expired image" {
		t.Fatalf("duplicate image did not become visible after the ten-minute window: %#v", got)
	}
	if got.Prompt != "keep this prompt" {
		t.Fatalf("native prompt card was changed or removed: %#v", got)
	}
}

func TestPatchDuplicateGeneratedImageRendererSkipsAmbiguousArtifactComponents(t *testing.T) {
	original := imagePreviewOriginalRendererFixture() + imageArtifactMarkdownRendererFixture() + imageArtifactMarkdownRendererFixture()
	updated, result := patchImagePreviewRenderer(original)
	if !result.Recognized || !result.Changed {
		t.Fatalf("preview fallback should still be patched: %#v", result)
	}
	if strings.Contains(updated, imageGenerationDedupePatchMarker) || strings.Contains(updated, `$wfImageDuplicate`) {
		t.Fatal("ambiguous Markdown component structure must not receive a guessed dedupe patch")
	}
}

func TestPatchImageGenerationUIWithMediaRendererRejectsMixedAliases(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace string
		with    string
	}{
		{
			name:    "generated image source belongs to another step",
			replace: `e.generatedImage?.base64Data`,
			with:    `other.generatedImage?.base64Data`,
		},
		{
			name:    "display name belongs to another resolved model",
			replace: `a=n?.displayName`,
			with:    `a=other?.displayName`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := strings.Replace(imageGenerationUIWithMediaRendererFixture(), test.replace, test.with, 1)
			if matches := findImageGenerationTitleRendererMatches(original); len(matches) != 0 {
				t.Fatalf("mixed aliases must not produce a patchable title match: %#v", matches)
			}
			updated, result := patchImagePreviewRenderer(original)
			if result.Recognized || result.Changed || updated != original {
				t.Fatalf("mixed aliases must remain untouched: result=%#v", result)
			}
		})
	}
}

func TestPatchImageGenerationUIRendererMigratesV2MarkerAndRemainsIdempotent(t *testing.T) {
	current, result := patchImagePreviewRenderer(imageGenerationUIRendererFixture())
	if !result.Changed {
		t.Fatal("fixture was not patched to the current UI version")
	}
	legacy := strings.Replace(current, imageGenerationUIPatchMarker, imageGenerationUIPatchV2Marker, 1)
	updated, result := patchImagePreviewRenderer(legacy)
	if !result.Recognized || !result.Changed || strings.Contains(updated, imageGenerationUIPatchV2Marker) || strings.Count(updated, imageGenerationUIPatchMarker) != 1 {
		t.Fatalf("v2 marker did not migrate to exactly one v3 marker: result=%#v source=%s", result, updated)
	}
	if second, secondResult := patchImagePreviewRenderer(updated); !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("migrated v3 UI renderer was not idempotent: result=%#v", secondResult)
	}
}

func TestPatchImageGenerationUIRendererPatchesUnfinishedComponentBesideV3(t *testing.T) {
	patched, result := patchImagePreviewRenderer(imageGenerationUIRendererFixture())
	if !result.Changed {
		t.Fatal("first fixture was not patched to v3")
	}
	// A renderer bundle can contain multiple image result components. A v3
	// marker on the first must not cause us to early-return and leave the second
	// native component collapsed/default-Gemini.
	source := patched + `;/* another renderer component */;` + imageGenerationUIRendererFixture()
	updated, result := patchImagePreviewRenderer(source)
	if !result.Recognized || !result.Changed || strings.Count(updated, imageGenerationUIPatchMarker) != 2 {
		t.Fatalf("unfinished sibling component was not patched beside v3: result=%#v source=%s", result, updated)
	}
	if second, secondResult := patchImagePreviewRenderer(updated); !secondResult.Recognized || secondResult.Changed || second != updated {
		t.Fatalf("multi-component v3 renderer was not idempotent: result=%#v", secondResult)
	}
}

func imageGenerationUIV1RendererFixture(t *testing.T) string {
	t.Helper()
	patched, result := patchImagePreviewRenderer(imageGenerationUIRendererFixture())
	if !result.Changed {
		t.Fatal("fixture was not patched to the current UI version")
	}
	legacy := strings.Replace(patched, imageGenerationUIPatchMarker, imageGenerationUIPatchV1Marker, 1)
	legacy = strings.Replace(legacy,
		imageGenerationModelLabel("e", "n", "a"),
		`a=n?.displayName||(e.modelName?.replace(/^gpt-image-(\d+)$/i,"GPT Image $1"))||e.modelName||"Image generator"`,
		1,
	)
	legacy = strings.Replace(legacy,
		`,i=n?.isNewModel??!1,$wfIsGeminiImage=!!n||/^gemini[-_]/i.test(e.modelName||"");return `,
		`,i=n?.isNewModel??!1;return `,
		1,
	)
	legacy = strings.Replace(legacy,
		`F("span",{children:Ga(t)&&!e.modelName?`+"`Generating image`:"+`Ga(t)?`,
		`F("span",{children:Ga(t)?`,
		1,
	)
	legacy = strings.ReplaceAll(legacy, `${$wfIsGeminiImage?" \u{1F34C}":""}`, `${n?" \u{1F34C}":""}`)
	return legacy
}

func TestPatchImagePreviewRendererUpgradesV7AndV1UIToV3(t *testing.T) {
	preview, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture())
	if !result.Changed {
		t.Fatal("preview fixture was not patched to the current fallback")
	}
	legacy := strings.Replace(preview, imagePreviewPatchMarker, imagePreviewPatchV7Marker, 1) + imageGenerationUIV1RendererFixture(t)
	updated, result := patchImagePreviewRenderer(legacy)
	if !result.Recognized || !result.Changed {
		t.Fatalf("v7/v1 renderer was not upgraded: %#v", result)
	}
	if strings.Contains(updated, imagePreviewPatchV7Marker) || strings.Contains(updated, imageGenerationUIPatchV1Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, imageGenerationUIPatchMarker) || !strings.Contains(updated, "`Generating image`") {
		t.Fatalf("v7/v1 renderer did not receive the full v8/v3 upgrade: %s", updated)
	}
	if _, result := patchImagePreviewRenderer(updated); !result.Recognized || result.Changed {
		t.Fatalf("v8/v3 renderer was not idempotent: %#v", result)
	}
}

func TestPatchImagePreviewRendererUpgradesV2(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imagePreviewV2RendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("v2 renderer was not upgraded: %#v", result)
	}
	if strings.Contains(updated, imagePreviewPatchV2Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, `startsWith("file://")`) {
		t.Fatalf("v2 upgrade did not produce the full v6 fallback: %s", updated)
	}
	assertImagePreviewJavaScriptSyntax(t, updated)
}

func TestPatchImagePreviewRendererUpgradesRealV3(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imagePreviewV3RendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("real v3 renderer was not upgraded: %#v", result)
	}
	if strings.Contains(updated, imagePreviewPatchV3Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, `typeof i==="string"`) {
		t.Fatalf("v3 upgrade did not produce the browser-safe v6 fallback: %s", updated)
	}
	if _, result := patchImagePreviewRenderer(updated); !result.Recognized || result.Changed {
		t.Fatalf("migrated v6 renderer was not idempotent: %#v", result)
	}
	assertImagePreviewJavaScriptSyntax(t, updated)
}

func TestPatchImagePreviewRendererUpgradesRealV4(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imagePreviewV4RendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("real v4 renderer was not upgraded: %#v", result)
	}
	if strings.Contains(updated, imagePreviewPatchV4Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, `typeof i==="string"`) {
		t.Fatalf("v4 upgrade did not produce the browser-safe v6 fallback: %s", updated)
	}
	if _, result := patchImagePreviewRenderer(updated); !result.Recognized || result.Changed {
		t.Fatalf("migrated v6 renderer was not idempotent: %#v", result)
	}
	assertImagePreviewJavaScriptSyntax(t, updated)
}

func TestPatchImagePreviewRendererUpgradesRealV5(t *testing.T) {
	updated, result := patchImagePreviewRenderer(imagePreviewV5RendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("real v5 renderer was not upgraded: %#v", result)
	}
	if strings.Contains(updated, imagePreviewPatchV5Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, `vscode-file://vscode-app`) {
		t.Fatalf("v5 upgrade did not produce the browser-safe v6 fallback: %s", updated)
	}
	if _, result := patchImagePreviewRenderer(updated); !result.Recognized || result.Changed {
		t.Fatalf("migrated v6 renderer was not idempotent: %#v", result)
	}
	assertImagePreviewJavaScriptSyntax(t, updated)
}

func TestPatchImagePreviewRendererPatchesEveryKnownOccurrence(t *testing.T) {
	source := imagePreviewOriginalRendererFixture() + `between;` + imagePreviewOriginalRendererFixture()
	updated, result := patchImagePreviewRenderer(source)
	if !result.Recognized || !result.Changed || strings.Count(updated, imagePreviewPatchMarker) != 2 {
		t.Fatalf("all known renderer occurrences must be patched: result=%#v source=%s", result, updated)
	}
	if _, result := patchImagePreviewRenderer(updated); !result.Recognized || result.Changed {
		t.Fatalf("multi-renderer v6 source was not idempotent: %#v", result)
	}
}

// Set ANTIGRAVITY_WF_TEST_RENDERERS to a path-list of real renderer bundles
// to verify an installed Antigravity version without mutating it. CI leaves it
// unset; release validation can point it at a freshly installed target.
func TestPatchImagePreviewRendererUpgradesOptionalInstalledRenderers(t *testing.T) {
	paths := filepath.SplitList(strings.TrimSpace(os.Getenv("ANTIGRAVITY_WF_TEST_RENDERERS")))
	if len(paths) == 0 || paths[0] == "" {
		t.Skip("ANTIGRAVITY_WF_TEST_RENDERERS is not set")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; installed-renderer syntax check skipped")
	}
	for _, rendererPath := range paths {
		rendererPath := rendererPath
		t.Run(filepath.Base(rendererPath), func(t *testing.T) {
			original, err := os.ReadFile(rendererPath)
			if err != nil {
				t.Fatal(err)
			}
			updated, result := patchImagePreviewRenderer(string(original))
			if !result.Recognized || !result.Changed || strings.Contains(updated, imagePreviewPatchV3Marker) || strings.Contains(updated, imagePreviewPatchV6Marker) || !strings.Contains(updated, imagePreviewPatchMarker) || !strings.Contains(updated, imageGenerationUIPatchMarker) {
				t.Fatalf("installed renderer was not safely migrated: %#v", result)
			}
			candidate := filepath.Join(t.TempDir(), filepath.Base(rendererPath))
			if err := os.WriteFile(candidate, []byte(updated), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", candidate).CombinedOutput(); err != nil {
				t.Fatalf("installed renderer migration failed node --check: %s: %v", output, err)
			}
		})
	}
}

func TestImagePreviewV4FallbackNormalizesWindowsAndMacFileURIs(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; JavaScript fallback execution check skipped")
	}
	renderer := imagePreviewV4Renderer("a", "e", "i", "n", "YI")
	for _, test := range []struct {
		name string
		uri  string
		want string
	}{
		{name: "windows drive", uri: "file:///C:/Users/%E6%97%A0%E9%A3%8E/image.png", want: "vscode-file://vscode-app/C:/Users/%E6%97%A0%E9%A3%8E/image.png"},
		{name: "mac absolute path", uri: "file:///Users/wufeng/image.png", want: "vscode-file://vscode-app/Users/wufeng/image.png"},
		{name: "literal percent remains renderable", uri: "file:///C:/Users/100%/image.png", want: "vscode-file://vscode-app/C:/Users/100%25/image.png"},
		{name: "windows UNC path", uri: "file://server/share/My%20Image.png", want: "vscode-file://server/share/My%20Image.png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "preview-uri.js")
			source := `"use strict";const e={generatedImage:{uri:` + strconv.Quote(test.uri) + `}};let a,i;const n=()=>undefined;const YI=()=>undefined;` + renderer + `;process.stdout.write(String(i));`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("transformed renderer failed node --check: %s: %v", output, err)
			}
			output, err := exec.Command(node, path).Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := string(output); got != test.want {
				t.Fatalf("normalized URI = %q, want %q", got, test.want)
			}
		})
	}
}

// TestPatchedImagePreviewRendererResolvesAllSupportedSourcesAtRuntime runs the
// exact renderer fragment produced by patchImagePreviewRenderer in Node. The
// structural tests above catch accidental changes to the emitted source; this
// test catches a more important class of regressions where syntactically valid
// fallback JavaScript still cannot provide the chat renderer with a usable
// image src for one of the media shapes emitted by different Antigravity
// versions.
func TestPatchedImagePreviewRendererResolvesAllSupportedSourcesAtRuntime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; patched renderer runtime check skipped")
	}

	patched, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture())
	if !result.Recognized || !result.Changed {
		t.Fatalf("fixture renderer was not patched: %#v", result)
	}

	for _, test := range []struct {
		name     string
		step     string
		resolver string
		want     string
	}{
		{
			name:     "generatedImage file URI with Windows Chinese user directory",
			step:     `{generatedImage:{uri:"file:///C:/Users/%E6%97%A0%E9%A3%8E/image.png"}}`,
			resolver: `()=>undefined`,
			want:     "vscode-file://vscode-app/C:/Users/%E6%97%A0%E9%A3%8E/image.png",
		},
		{
			name:     "generatedMedia payload inline data",
			step:     `{generatedMedia:{payload:{case:"inlineData",inlineData:{mimeType:"image/webp",data:"aGVsbG8="}}}}`,
			resolver: `()=>undefined`,
			want:     "data:image/webp;base64,aGVsbG8=",
		},
		{
			name:     "generatedMedia string base64 data",
			step:     `{generatedMedia:{mimeType:"image/jpeg",base64Data:"c3RyaW5nLWJ5dGVz"}}`,
			resolver: `()=>undefined`,
			want:     "data:image/jpeg;base64,c3RyaW5nLWJ5dGVz",
		},
		{
			name:     "generatedMedia byte array base64 data",
			step:     `{generatedMedia:{mimeType:"image/png",base64Data:[72,105]}}`,
			resolver: `()=>undefined`,
			want:     "data:image/png;base64,SGk=",
		},
		{
			name:     "artifact resolver Store getState",
			step:     `{generatedMedia:{uri:"artifact://conversation/image-1"}}`,
			resolver: `uri=>({getState:()=>"blob:preview-from-store:"+uri})`,
			want:     "blob:preview-from-store:artifact://conversation/image-1",
		},
		{
			name:     "generatedImage is tried after metadata-only generatedMedia",
			step:     `{generatedMedia:{mimeType:"image/png"},generatedImage:{uri:"file:///C:/Users/%E6%97%A0%E9%A3%8E/image.png"}}`,
			resolver: `()=>undefined`,
			want:     "vscode-file://vscode-app/C:/Users/%E6%97%A0%E9%A3%8E/image.png",
		},
		{
			name:     "file URI falls back after no-op artifact resolver Promise",
			step:     `{generatedImage:{uri:"file:///C:/Users/%E6%97%A0%E9%A3%8E/image.png"}}`,
			resolver: `()=>Promise.resolve(undefined)`,
			want:     "vscode-file://vscode-app/C:/Users/%E6%97%A0%E9%A3%8E/image.png",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "patched-preview-runtime.js")
			// Keep the surrounding declarations deliberately close to the known
			// minified renderer: patchImagePreviewRenderer changes only the
			// expression between prefix and let s, so this executes the patched
			// source rather than a separately reconstructed fallback.
			source := `"use strict";const e=` + test.step + `;let a,i;const n=` + test.resolver + `;const YI=media=>{const data=media?.payload?.inlineData;return data?"data:"+(data.mimeType||"image/png")+";base64,"+data.data:void 0};const Ia=()=>false;const t={};const prefix=0,suffix=0;` + patched + `;process.stdout.write(JSON.stringify(i));`
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("patched renderer failed node --check: %s: %v", output, err)
			}
			output, err := exec.Command(node, path).CombinedOutput()
			if err != nil {
				t.Fatalf("patched renderer failed at runtime: %s: %v", output, err)
			}
			var got string
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("patched renderer did not return a JSON image src %q: %v", output, err)
			}
			if got != test.want {
				t.Fatalf("image src = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPatchImagePreviewRendererSkipsUnknownRenderer(t *testing.T) {
	original := `const futureRenderer={generatedMedia:"different-shape"};`
	updated, result := patchImagePreviewRenderer(original)
	if result.Recognized || result.Changed || updated != original {
		t.Fatalf("unknown renderer must remain untouched: result=%#v updated=%q", result, updated)
	}
}

func TestImagePreviewRendererPathsOnlyIncludeKnownExistingBundles(t *testing.T) {
	root := t.TempDir()
	for _, relative := range imagePreviewRendererRelativePaths[:2] {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(imagePreviewOriginalRendererFixture()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "out", "unrelated.js"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := imagePreviewRendererPaths(root)
	if len(paths) != 2 {
		t.Fatalf("unexpected renderer paths: %v", paths)
	}
}

func TestImagePreviewRenderersNeedPatchOnlyForKnownOutdatedBundles(t *testing.T) {
	root := t.TempDir()
	unknown := filepath.Join(root, "future-renderer.js")
	known := filepath.Join(root, "known-renderer.js")
	missing := filepath.Join(root, "missing-renderer.js")
	if err := os.WriteFile(unknown, []byte(`const futureRenderer={generatedMedia:"different-shape"};`), 0o644); err != nil {
		t.Fatal(err)
	}
	if imagePreviewRenderersNeedPatch([]string{missing, unknown}) {
		t.Fatal("missing or unknown renderers must not make the target pending")
	}
	if err := os.WriteFile(known, []byte(imagePreviewOriginalRendererFixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	if !imagePreviewRenderersNeedPatch([]string{missing, unknown, known}) {
		t.Fatal("recognized unpatched 1.23.2 renderer must make the target pending")
	}
	updated, result := patchImagePreviewRenderer(imagePreviewOriginalRendererFixture())
	if !result.Changed {
		t.Fatal("fixture should produce a v4 renderer")
	}
	if err := os.WriteFile(known, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if imagePreviewRenderersNeedPatch([]string{missing, unknown, known}) {
		t.Fatal("v4, missing, and unknown renderers must not make the target pending")
	}
}

func TestWindowsTargetPatchStateRequiresKnownPendingPreviewFallback(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "resources", "app", "out", "main.js")
	if err := os.MkdirAll(filepath.Dir(main), 0o755); err != nil {
		t.Fatal(err)
	}
	endpointPatched := "// " + windowsMainMarker + "\n" + windowsBaseProxyEndpoint + "\n"
	if err := os.WriteFile(main, []byte(endpointPatched+imagePreviewOriginalRendererFixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	target := windowsTarget{root: root, kind: "ide", main: main}
	mainPatched, extensionPatched, languagePatched, fullyPatched := windowsTargetPatchState(target)
	if !mainPatched || !extensionPatched || !languagePatched || fullyPatched {
		t.Fatalf("known pending preview fallback must keep an otherwise patched target pending: main=%t extension=%t language=%t fully=%t", mainPatched, extensionPatched, languagePatched, fullyPatched)
	}

	updated, result := patchImagePreviewRenderer(endpointPatched + imagePreviewOriginalRendererFixture())
	if !result.Changed {
		t.Fatal("fixture should produce the current v4 renderer")
	}
	if err := os.WriteFile(main, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, fullyPatched = windowsTargetPatchState(target); !fullyPatched {
		t.Fatal("v4 renderer should restore the complete target status")
	}

	if err := os.WriteFile(main, []byte(endpointPatched+`const futureRenderer={generatedMedia:"different-shape"};`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, fullyPatched = windowsTargetPatchState(target); !fullyPatched {
		t.Fatal("unknown or absent optional renderers must not block normal endpoint status")
	}
}

func TestWindowsAgentTargetPatchStateRequiresKnownPendingPreviewFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := fixture.write(path, map[string][]byte{
		"dist/main.js":            []byte(`"use strict";` + "\n// " + windowsASARMarker),
		"dist/languageServer.js":  []byte(`const args=["--cloud_code_endpoint","` + windowsBaseProxyEndpoint + `"];`),
		"out/jetskiAgent/main.js": []byte(imagePreviewOriginalRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	target := windowsTarget{kind: "agent", asar: path}
	mainPatched, extensionPatched, languagePatched, fullyPatched := windowsTargetPatchState(target)
	if !mainPatched || !extensionPatched || !languagePatched || fullyPatched {
		t.Fatalf("known pending Agent renderer must keep an otherwise patched target pending: main=%t extension=%t language=%t fully=%t", mainPatched, extensionPatched, languagePatched, fullyPatched)
	}

	archive, err := readASAR(path)
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{}
	if !patchImagePreviewASARRenderers(archive, replacements) {
		t.Fatal("fixture ASAR should require the current v4 renderer fallback")
	}
	patchedPath := filepath.Join(filepath.Dir(path), "patched.app.asar")
	if err := archive.write(patchedPath, replacements); err != nil {
		t.Fatal(err)
	}
	target.asar = patchedPath
	if _, _, _, fullyPatched = windowsTargetPatchState(target); !fullyPatched {
		t.Fatal("v4 Agent renderer should restore the complete target status")
	}
}

func TestPatchImagePreviewASARRenderersUpdatesKnownEntriesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := fixture.write(path, map[string][]byte{
		"dist/main.js":                               []byte(imagePreviewOriginalRendererFixture()),
		"out/jetskiAgent/main.js":                    []byte(imagePreviewOriginalRendererFixture()),
		"out/vs/workbench/workbench.desktop.main.js": []byte(`const unrelated=true;`),
		"dist/unrelated.js":                          []byte(imagePreviewOriginalRendererFixture()),
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := readASAR(path)
	if err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{}
	if !patchImagePreviewASARRenderers(archive, replacements) {
		t.Fatal("known ASAR renderer entries were not patched")
	}
	if len(replacements) != 2 {
		t.Fatalf("only known matching renderer entries should change: %v", replacements)
	}
	for name, data := range replacements {
		if !strings.Contains(string(data), imagePreviewPatchMarker) {
			t.Fatalf("%s did not receive v4 preview fallback", name)
		}
	}
}

func assertImagePreviewJavaScriptSyntax(t *testing.T, renderer string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; JavaScript syntax check skipped")
	}
	path := filepath.Join(t.TempDir(), "preview-renderer.js")
	// The wrapper supplies the minified identifiers used by the known renderer
	// shape while leaving the transformed source otherwise unchanged.
	source := `"use strict";const e={generatedMedia:{uri:"file:///Users/test.png"}};const n=()=>undefined;const YI=()=>undefined;let a,i;` + renderer
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
		t.Fatalf("transformed renderer failed node --check: %s: %v", output, err)
	}
}

// TestOfficialIDEImageRendererWhenFixturePresent validates the exact renderer
// bytes from an official IDE application without modifying the installation.
// The app root must be the directory that directly contains out/ and
// product.json (resources/app on Windows or Contents/Resources/app on macOS).
func TestOfficialIDEImageRendererWhenFixturePresent(t *testing.T) {
	root := os.Getenv("ANTIGRAVITY_WF_TEST_IDE_APP_ROOT")
	if root == "" {
		t.Skip("official IDE renderer fixture is not configured")
	}
	paths := []string{
		filepath.Join(root, "out", "jetskiAgent", "main.js"),
		filepath.Join(root, "out", "vs", "workbench", "workbench.desktop.main.js"),
	}
	if len(paths) != 2 {
		t.Fatalf("official IDE fixture must contain both chat renderers: %v", paths)
	}
	for _, path := range paths {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		updated, result := patchImagePreviewRenderer(string(original))
		if !result.Recognized || !result.Changed {
			t.Fatalf("official IDE renderer is not safely patchable: %s %#v", path, result)
		}
		previewReady := strings.Contains(updated, imagePreviewPatchMarker) ||
			strings.Contains(updated, imagePreviewNativeCompatibleMarker)
		if !previewReady {
			t.Fatalf("official IDE renderer has neither the managed fallback nor a verified native preview after patch: %s", path)
		}
		for _, marker := range []string{imageGenerationUIPatchMarker, imageGenerationDedupePatchMarker} {
			if !strings.Contains(updated, marker) {
				t.Fatalf("official IDE renderer is missing %s after patch: %s", marker, path)
			}
		}
		second, secondResult := patchImagePreviewRenderer(updated)
		if !secondResult.Recognized || secondResult.Changed || second != updated {
			t.Fatalf("official IDE renderer patch is not idempotent: %s %#v", path, secondResult)
		}
		if !bytes.Equal(original, mustReadImagePreviewFixture(t, path)) {
			t.Fatalf("read-only renderer validation modified the fixture: %s", path)
		}
		if node, err := exec.LookPath("node"); err == nil {
			candidate := filepath.Join(t.TempDir(), filepath.Base(path))
			if err := os.WriteFile(candidate, []byte(updated), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", candidate).CombinedOutput(); err != nil {
				t.Fatalf("patched official renderer failed node --check: %s: %v", output, err)
			}
		}
	}
}

func mustReadImagePreviewFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
