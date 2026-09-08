package patcher

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// imagePreviewPatchMarker identifies the current renderer fallback that makes
// a generated-image tool result visible even when a newer Antigravity build
// stores it in generatedImage, base64Data, or a local file URI rather than
// the older generatedMedia URI shape.
//
// Keep this marker versioned. v3 was shipped by an earlier helper build, but
// its Windows file URI conversion leaves a leading slash before drive letters
// ("/C:/...") and therefore cannot render the affected Chinese-home-directory
// paths. v6 additionally maps local file URIs to the VS Code browser-resource
// scheme allowed by the workbench CSP. v7 also corrects the matching native
// image-tool UI: it exposes the actual image model and opens the image result
// by default without changing the upstream media payload. v8 makes the
// in-progress title neutral until a real model is present and preserves
// generic Gemini model names such as gemini-3.6-flash.
const imagePreviewPatchMarker = "antigravity-wf:image-preview-fallback:v8"

// Newer IDE renderers already resolve generatedMedia through the workbench
// artifact resolver and render a visible "Generated image preview". They do
// not contain the legacy expression that needs imagePreviewPatchMarker. Mark
// only the fully validated native component so Windows status can distinguish
// a compatible renderer from an unknown future layout without injecting a
// fake fallback block.
const imagePreviewNativeCompatibleMarker = "antigravity-wf:image-preview-native:v1"

const imagePreviewPatchV7Marker = "antigravity-wf:image-preview-fallback:v7"

const imagePreviewPatchV6Marker = "antigravity-wf:image-preview-fallback:v6"

// UI v3 adds a strict matcher for the macOS 1.23.2 title component and keeps
// scanning a bundle even when another image component was already patched.
// That avoids silently leaving a second renderer component at its stock,
// collapsed/default-Gemini behaviour.
const imageGenerationUIPatchMarker = "antigravity-wf:image-generation-ui:v3"

// v3 is the current cross-platform renderer patch. Keep the explicit alias
// used by Windows status/migration code so an already patched installation is
// recognised without downgrading it.
const imageGenerationUIPatchV3Marker = imageGenerationUIPatchMarker

const imageGenerationUIPatchV2Marker = "antigravity-wf:image-generation-ui:v2"

const imageGenerationUIPatchV1Marker = "antigravity-wf:image-generation-ui:v1"

// imageGenerationDedupePatchMarker is intentionally independent from the
// image-generation UI marker. The native image-tool card remains intact (and
// therefore keeps the user's prompt); this marker applies only to the
// duplicate Markdown artifact image that some IDE builds append to the same
// chat turn. v2 records a URI timestamp rather than a permanent Set entry.
// v3 also hides an already-mounted matching artifact node. v4 additionally
// consumes one recent generated-image event when Antigravity saves the same
// bytes under a different artifact URI, which is the shape used by IDE 2.5.5
// and Agent 2.8.1. v5 mirrors only anonymous timestamps and URI fingerprints
// through localStorage so the native card and artifact component can correlate
// even when Antigravity runs them in separate renderer globals. The event is
// single-use, so an unrelated later Markdown image remains visible. v6 also
// constrains the verified duplicate artifact component to a 320px thumbnail
// when an Antigravity renderer still mounts it despite the event suppression.
const imageGenerationDedupePatchMarker = "antigravity-wf:image-generation-dedupe:v6"

const imageGenerationDedupePatchV5Marker = "antigravity-wf:image-generation-dedupe:v5"

const imageGenerationDedupePatchV4Marker = "antigravity-wf:image-generation-dedupe:v4"

const imageGenerationDedupePatchV3Marker = "antigravity-wf:image-generation-dedupe:v3"

// Windows previously called the current timestamped registry revision v2.
// Retain the alias for idempotence checks and older test/restore paths.
const imageGenerationDedupePatchV2Marker = "antigravity-wf:image-generation-dedupe:v2"

const imagePreviewPatchV5Marker = "antigravity-wf:image-preview-fallback:v5"

const imagePreviewPatchV4Marker = "antigravity-wf:image-preview-fallback:v4"

const imagePreviewPatchV3Marker = "antigravity-wf:image-preview-fallback:v3"

const imagePreviewPatchV2Marker = "antigravity-wf:image-preview-fallback:v2"

var imagePreviewRendererRelativePaths = []string{
	"out/main.js",
	"out/jetskiAgent/main.js",
	"out/vs/workbench/workbench.desktop.main.js",
}

// Packaged Agent builds do not consistently use the unpacked IDE paths. Keep
// the known renderer entries explicit rather than walking or rewriting
// arbitrary JavaScript in an ASAR archive.
var imagePreviewASARRelativePaths = []string{
	"dist/main.js",
	"dist/jetskiAgent/main.js",
	"dist/vs/workbench/workbench.desktop.main.js",
	"out/main.js",
	"out/jetskiAgent/main.js",
	"out/vs/workbench/workbench.desktop.main.js",
}

// This is the unmodified Antigravity 1.23.2 image-generation renderer shape.
// It is intentionally strict: a future renderer that does not match is left
// untouched, so an image-preview compatibility improvement can never block
// the normal local-proxy endpoint patch.
var imagePreviewOriginalRendererPattern = regexp.MustCompile(
	`([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\.generatedMedia,([A-Za-z_$][A-Za-z0-9_$]*);` +
		`([A-Za-z_$][A-Za-z0-9_$]*)\?\.uri\?([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\?\.\(([A-Za-z_$][A-Za-z0-9_$]*)\.uri\)\|\|void 0:` +
		`([A-Za-z_$][A-Za-z0-9_$]*)\?\.payload\.case==="inlineData"&&\(([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\?([A-Za-z_$][A-Za-z0-9_$]*)\(([A-Za-z_$][A-Za-z0-9_$]*)\):void 0\);`,
)

// Legacy v2/v3 blocks were produced by this application, so they are allowed
// to be migrated only after their complete generated-image expression has been
// structurally recognised. Do not perform a broad marker-only substitution:
// a renderer could contain more than one matching expression and a marker for
// one must never hide another unfinished expression.
var imagePreviewLegacyHeaderPattern = regexp.MustCompile(
	`^/\*antigravity-wf:image-preview-fallback:(v2|v3|v4|v5|v6|v7)\*/` +
		`([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\.generatedMedia\|\|([A-Za-z_$][A-Za-z0-9_$]*)\.generatedImage,([A-Za-z_$][A-Za-z0-9_$]*);`,
)

var imagePreviewLegacyResolverPattern = regexp.MustCompile(
	`([A-Za-z_$][A-Za-z0-9_$]*)\?\.uri\?\(([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\?\.\(([A-Za-z_$][A-Za-z0-9_$]*)\.uri\),`,
)

var imagePreviewLegacyInlinePattern = regexp.MustCompile(
	`([A-Za-z_$][A-Za-z0-9_$]*)\?\.payload\?\.case==="inlineData"&&\(([A-Za-z_$][A-Za-z0-9_$]*)=([A-Za-z_$][A-Za-z0-9_$]*)\?([A-Za-z_$][A-Za-z0-9_$]*)\(([A-Za-z_$][A-Za-z0-9_$]*)\):void 0\)`,
)

var imagePreviewLegacyBlockEndPattern = regexp.MustCompile(`;let\s+`)

const imagePreviewJavaScriptIdentifier = `[A-Za-z_$][A-Za-z0-9_$]*`

// This matches only the renderer expression emitted by imagePreviewV4Renderer
// (the current v8 marker). It is used to attach generated-image URI
// registration immediately after an already validated native preview
// expression; a marker alone is never sufficient to patch unrelated code.
var imagePreviewCurrentHeaderPattern = regexp.MustCompile(
	`^/\*` + regexp.QuoteMeta(imagePreviewPatchMarker) + `\*/` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\|\|(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage,(` + imagePreviewJavaScriptIdentifier + `);`,
)

var imageGenerationLegacyDuplicateExpressionPattern = regexp.MustCompile(
	`\$wfImageDuplicate=!!globalThis\.__antigravityWFIsRecentGeneratedImageV2&&\[(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `)\]\.some\(value=>globalThis\.__antigravityWFIsRecentGeneratedImageV2\(value\)\),`,
)

var imageGenerationV4DuplicateExpressionPattern = regexp.MustCompile(
	`\$wfImageDuplicate=!!globalThis\.__antigravityWFConsumeGeneratedImageV4&&globalThis\.__antigravityWFConsumeGeneratedImageV4\((` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `)\),`,
)

// imageGenerationTitleRendererPattern describes one exact native title
// component form. The minified identifier aliases are deliberately captured
// separately: Go's regexp implementation does not support backreferences, so
// we validate every alias after matching rather than silently accepting a
// title expression that mixes values from separate components.
type imageGenerationTitleRendererPattern struct {
	expression             *regexp.Regexp
	componentGroup         int
	stepGroup              int
	statusGroup            int
	stepReferenceGroups    []int
	resolvedModelGroup     int
	resolvedModelRefGroups []int
	displayNameGroup       int
	isNewModelGroup        int
}

// These two forms match the complete native image-tool title components
// observed in Antigravity 1.23.2. They deliberately include the surrounding
// JSX shape so a similarly named string elsewhere in a future renderer cannot
// be changed accidentally. The first form is used by the known Windows
// renderer. The second is the real macOS 1.23.2 form, whose availability value
// checks generatedMedia as well as generatedImage.
var imageGenerationTitleRendererPatterns = []imageGenerationTitleRendererPattern{
	{
		expression: regexp.MustCompile(
			`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let ` +
				`(` + imagePreviewJavaScriptIdentifier + `)=!!(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.uri,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|"Gemini",` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1;return `,
		),
		componentGroup:         1,
		stepGroup:              2,
		statusGroup:            3,
		stepReferenceGroups:    []int{5, 7, 9},
		resolvedModelGroup:     6,
		resolvedModelRefGroups: []int{11, 13},
		displayNameGroup:       10,
		isNewModelGroup:        12,
	},
	{
		expression: regexp.MustCompile(
			`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let ` +
				`(` + imagePreviewJavaScriptIdentifier + `)=!!\((` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.uri\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.payload\?\.value\?\.length\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.uri\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.base64Data\),` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|"Gemini",` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1;return `,
		),
		componentGroup:         1,
		stepGroup:              2,
		statusGroup:            3,
		stepReferenceGroups:    []int{5, 6, 7, 8, 10, 12},
		resolvedModelGroup:     9,
		resolvedModelRefGroups: []int{14, 16},
		displayNameGroup:       13,
		isNewModelGroup:        15,
	},
}

var imageGenerationResultRendererPattern = regexp.MustCompile(
	`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `),error:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>` +
		`(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `),\{loading:(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `)\),title:(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `),\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\),supplementaryView:(` + imagePreviewJavaScriptIdentifier + `)\?null:(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `),\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\),cta:null\}\)`,
)

var imageGenerationExpansionHooksPattern = regexp.MustCompile(
	`\[(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `)\]=(` + imagePreviewJavaScriptIdentifier + `)\(!1\),` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\(\(\)=>\{(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `)=>!(` + imagePreviewJavaScriptIdentifier + `)\)\},\[\]\)`,
)

// Antigravity IDE 2.1.x combines the generated-image title and result
// container into one component. These patterns intentionally require the
// complete model-resolution prefix before any rewrite. The second shape is
// the macOS multi-source variant: generatedMedia, generatedImage, payload and
// base64 availability are all tied to the same step alias before it can be
// changed.
type imageGenerationCombinedRendererPattern struct {
	expression             *regexp.Regexp
	componentGroup         int
	stepGroup              int
	statusGroup            int
	hasMediaGroup          int
	stepReferenceGroups    []int
	resolvedModelGroup     int
	resolvedModelRefGroups []int
	displayNameGroup       int
	isNewModelGroup        int
	titleGroup             int
}

var imageGenerationCombinedRendererPatterns = []imageGenerationCombinedRendererPattern{
	{
		expression: regexp.MustCompile(
			`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `),error:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let ` +
				`(` + imagePreviewJavaScriptIdentifier + `)=!!(` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.uri,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|"Gemini",` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=`,
		),
		componentGroup:         1,
		stepGroup:              2,
		statusGroup:            3,
		hasMediaGroup:          5,
		stepReferenceGroups:    []int{6, 8, 10},
		resolvedModelGroup:     7,
		resolvedModelRefGroups: []int{12, 14},
		displayNameGroup:       11,
		isNewModelGroup:        13,
		titleGroup:             15,
	},
	{
		expression: regexp.MustCompile(
			`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `),error:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let ` +
				`(` + imagePreviewJavaScriptIdentifier + `)=!!\((` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.uri\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.payload\?\.value\?\.length\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.uri\|\|` +
				`(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.base64Data\),` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|"Gemini",` +
				`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1,` +
				`(` + imagePreviewJavaScriptIdentifier + `)=`,
		),
		componentGroup:         1,
		stepGroup:              2,
		statusGroup:            3,
		hasMediaGroup:          5,
		stepReferenceGroups:    []int{6, 7, 8, 9, 11, 13},
		resolvedModelGroup:     10,
		resolvedModelRefGroups: []int{15, 17},
		displayNameGroup:       14,
		isNewModelGroup:        16,
		titleGroup:             18,
	},
}

// This matches the title component emitted by the v1 UI patch, not arbitrary
// workbench code. The surrounding marker is checked separately before a
// migration is allowed.
var imageGenerationLegacyUITitleRendererPattern = regexp.MustCompile(
	`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let ` +
		`(` + imagePreviewJavaScriptIdentifier + `)=!!(` + imagePreviewJavaScriptIdentifier + `)\.generatedImage\?\.uri,` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|\((` + imagePreviewJavaScriptIdentifier + `)\.modelName\?\.replace\(/\^gpt-image-\(\\d\+\)\$/i,"GPT Image \$1"\)\)\|\|(` + imagePreviewJavaScriptIdentifier + `)\.modelName\|\|"Image generator",` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1;return `,
)

var imageGenerationTitleChildrenPattern = regexp.MustCompile(
	`(` + imagePreviewJavaScriptIdentifier + `)\("span",\{children:(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `)\)\?`,
)

// This is the dedicated Markdown artifact-image component used by the IDE.
// It is paired with an already-recognised native generated-image preview
// before any rewrite is allowed, so ordinary Markdown images stay untouched.
var imageArtifactMarkdownRendererPrefixPattern = regexp.MustCompile(
	`(` + imagePreviewJavaScriptIdentifier + `)=\(\{src:(` + imagePreviewJavaScriptIdentifier + `),alt:(` + imagePreviewJavaScriptIdentifier + `),originalFilePath:(` + imagePreviewJavaScriptIdentifier + `),popout:(` + imagePreviewJavaScriptIdentifier + `)=!0,className:(` + imagePreviewJavaScriptIdentifier + `)="",openUri:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{let\[(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `)\]=(` + imagePreviewJavaScriptIdentifier + `)\(!1\),(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `)\),`,
)

var imagePreviewNativeRendererPrefixPattern = regexp.MustCompile(
	`(` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{`,
)

type imagePreviewNativeRendererMatch struct {
	start     int
	prefixEnd int
	end       int
	step      string
	media     string
}

type imagePreviewPatchResult struct {
	Recognized bool
	Changed    bool
}

func patchImagePreviewRenderer(source string) (string, imagePreviewPatchResult) {
	result := imagePreviewPatchResult{Recognized: strings.Contains(source, imagePreviewPatchMarker) || strings.Contains(source, imagePreviewNativeCompatibleMarker)}

	var changed bool
	source, result.Recognized, changed = upgradeLegacyImagePreviewRenderers(source, result.Recognized)
	result.Changed = changed

	updated, recognized, changed := patchOriginalImagePreviewRenderers(source)
	source = updated
	result.Recognized = result.Recognized || recognized
	result.Changed = result.Changed || changed

	updated, recognized, changed = markNativeImagePreviewRenderers(source)
	source = updated
	result.Recognized = result.Recognized || recognized
	result.Changed = result.Changed || changed

	updated, recognized, changed = patchImageGenerationUIRenderers(source)
	source = updated
	result.Recognized = result.Recognized || recognized
	result.Changed = result.Changed || changed

	updated, recognized, changed = patchDuplicateGeneratedImageRenderers(source)
	source = updated
	result.Recognized = result.Recognized || recognized
	result.Changed = result.Changed || changed
	return source, result
}

func markNativeImagePreviewRenderers(source string) (string, bool, bool) {
	matches := findNativeImagePreviewRendererMatches(source)
	if len(matches) != 1 {
		return source, strings.Contains(source, imagePreviewNativeCompatibleMarker), false
	}
	match := matches[0]
	marker := "/*" + imagePreviewNativeCompatibleMarker + "*/"
	if match.start >= len(marker) && source[match.start-len(marker):match.start] == marker {
		return source, true, false
	}
	return source[:match.start] + marker + source[match.start:], true, true
}

func findNativeImagePreviewRendererMatches(source string) []imagePreviewNativeRendererMatch {
	var matches []imagePreviewNativeRendererMatch
	for _, match := range imagePreviewNativeRendererPrefixPattern.FindAllStringSubmatchIndex(source, -1) {
		step := imagePreviewSubmatch(source, match, 2)
		if step == "" || match[1] <= 0 || source[match[1]-1] != '{' {
			continue
		}
		end, ok := imagePreviewJavaScriptBalancedBlockEnd(source, match[1]-1)
		if !ok || end-match[0] > 16*1024 {
			continue
		}
		component := source[match[0]:end]
		mediaPattern := regexp.MustCompile(`(` + imagePreviewJavaScriptIdentifier + `)=` + regexp.QuoteMeta(step) + `\.generatedMedia,`)
		mediaMatches := mediaPattern.FindAllStringSubmatch(component, -1)
		if len(mediaMatches) != 1 {
			continue
		}
		media := mediaMatches[0][1]
		required := []string{
			"stepHandler:{openFile:", "resolveArtifactUrl:",
			media + `?.uri`, `payload.case==="inlineData"`,
			`alt:"Generated image preview"`, `children:"Prompt"`,
		}
		valid := true
		for _, token := range required {
			if !strings.Contains(component, token) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		matches = append(matches, imagePreviewNativeRendererMatch{
			start: match[0], prefixEnd: match[1], end: end, step: step, media: media,
		})
	}
	return matches
}

// patchDuplicateGeneratedImageRenderers keeps the native generated-image card
// (including its prompt) and hides only the matching duplicate rendered by
// the verified Markdown artifact component. It is deliberately all-or-nothing
// for this optional behaviour: an unknown or ambiguous component is left
// untouched rather than guessing where a normal Markdown image begins.
func patchDuplicateGeneratedImageRenderers(source string) (string, bool, bool) {
	if upgraded, recognized, changed := upgradeLegacyImageDedupeRenderers(source); recognized {
		source = upgraded
		if strings.Contains(source, imageGenerationDedupePatchMarker) {
			return source, true, changed
		}
		return source, true, false
	}
	if strings.Contains(source, imageGenerationDedupePatchMarker) {
		return source, true, false
	}
	registrations := generatedImageRegistrationReplacements(source)
	markdown := imageArtifactMarkdownReplacement(source)
	if len(registrations) == 0 || markdown == nil {
		return source, false, false
	}
	replacements := append(registrations, *markdown)
	sort.Slice(replacements, func(left, right int) bool {
		return replacements[left].start < replacements[right].start
	})
	var output strings.Builder
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			return source, false, false
		}
		output.WriteString(source[last:replacement.start])
		output.WriteString(replacement.value)
		last = replacement.end
	}
	output.WriteString(source[last:])
	return output.String(), true, true
}

// upgradeLegacyImageDedupeRenderers migrates only the exact v2 runtime emitted
// by this project. It does not remove or replace arbitrary JavaScript around a
// marker. The component continues using the stable V2 global ABI while the
// remember function gains the mounted-node cleanup needed by v3.
func upgradeLegacyImageDedupeRenderers(source string) (string, bool, bool) {
	if strings.Contains(source, imageGenerationDedupePatchV5Marker) {
		updated := strings.ReplaceAll(source, imageGenerationDedupePatchV5Marker, imageGenerationDedupePatchMarker)
		updated, ok := addImageArtifactThumbnailToMarkedComponent(updated)
		if !ok {
			return source, true, false
		}
		return updated, true, updated != source
	}
	marker := ""
	legacyRuntime := ""
	switch {
	case strings.Contains(source, imageGenerationDedupePatchV4Marker):
		marker = imageGenerationDedupePatchV4Marker
		legacyRuntime = `globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
			generatedImageMountedDuplicateHiderDefinition() + generatedImageV4RememberDefinition() +
			generatedImageIsRecentDefinition() + generatedImageV4ConsumeDefinition()
	case strings.Contains(source, imageGenerationDedupePatchV3Marker):
		marker = imageGenerationDedupePatchV3Marker
		legacyRuntime = generatedImageV3MountedDuplicateHiderDefinition() + generatedImageV3RememberDefinition()
	case strings.Contains(source, imageGenerationDedupePatchV2Marker):
		marker = imageGenerationDedupePatchV2Marker
		legacyRuntime = generatedImageLegacyRememberDefinition()
	default:
		return source, false, false
	}
	if !strings.Contains(source, legacyRuntime) {
		return source, true, false
	}
	currentRuntime := `globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageMountedDuplicateHiderDefinition() + generatedImageRememberDefinition()
	if marker == imageGenerationDedupePatchV4Marker {
		currentRuntime += generatedImageIsRecentDefinition() + generatedImageConsumeDefinition()
	}
	updated := strings.ReplaceAll(source, legacyRuntime, currentRuntime)
	if !strings.Contains(updated, generatedImageIsRecentDefinition()) {
		return source, true, false
	}
	if marker == imageGenerationDedupePatchV4Marker {
		currentExpressions := imageGenerationV4DuplicateExpressionPattern.FindAllStringSubmatchIndex(updated, -1)
		if len(currentExpressions) != 1 {
			return source, true, false
		}
		updated = imageGenerationV4DuplicateExpressionPattern.ReplaceAllString(updated,
			`$$wfImageDuplicate=(`+generatedImageConsumerRuntime()+`!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4($1,$2,$3)),`)
	} else {
		updated = strings.Replace(updated, generatedImageIsRecentDefinition(), generatedImageIsRecentDefinition()+generatedImageConsumeDefinition(), 1)
	}
	legacyExpressions := imageGenerationLegacyDuplicateExpressionPattern.FindAllStringSubmatchIndex(updated, -1)
	if marker != imageGenerationDedupePatchV4Marker {
		if len(legacyExpressions) != 1 {
			return source, true, false
		}
		updated = imageGenerationLegacyDuplicateExpressionPattern.ReplaceAllString(updated,
			`$$wfImageDuplicate=(`+generatedImageConsumerRuntime()+`!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4($1,$2,$3)),`)
	}
	updated = strings.ReplaceAll(updated, marker, imageGenerationDedupePatchMarker)
	var thumbnailOK bool
	updated, thumbnailOK = addImageArtifactThumbnailToMarkedComponent(updated)
	if !thumbnailOK {
		return source, true, false
	}
	return updated, true, updated != source
}

const imageArtifactThumbnailStyle = `style:{maxWidth:"320px",maxHeight:"320px",width:"auto",height:"auto",objectFit:"contain"},`

func addImageArtifactThumbnailToMarkedComponent(source string) (string, bool) {
	marker := "/*" + imageGenerationDedupePatchMarker + "*/"
	markerStart := strings.Index(source, marker)
	if markerStart < 0 || strings.Index(source[markerStart+len(marker):], marker) >= 0 {
		return source, false
	}
	componentStart := markerStart + len(marker)
	openOffset := strings.Index(source[componentStart:], "=>{")
	if openOffset < 0 || openOffset > 2*1024 {
		return source, false
	}
	componentEnd, ok := imagePreviewJavaScriptBalancedBlockEnd(source, componentStart+openOffset+2)
	if !ok || componentEnd-componentStart > 16*1024 {
		return source, false
	}
	component := source[componentStart:componentEnd]
	if !strings.Contains(component, `$wfImageDuplicate=`) || !strings.Contains(component, `__antigravityWFConsumeGeneratedImageV4`) {
		return source, false
	}
	if strings.Contains(component, imageArtifactThumbnailStyle) {
		return source, true
	}
	thumbnailPattern := regexp.MustCompile(`alt:` + imagePreviewJavaScriptIdentifier + `\|\|"Artifact image",`)
	matches := thumbnailPattern.FindAllStringIndex(component, -1)
	if len(matches) != 1 {
		return source, false
	}
	insert := componentStart + matches[0][1]
	return source[:insert] + imageArtifactThumbnailStyle + source[insert:], true
}

// generatedImageRegistrationReplacements adds a short-lived record for every
// renderer expression that has already passed the v8 structural matcher. The
// key normalises only equivalent file/browser-resource URI wrappers and keeps
// path case intact: case-sensitive macOS volumes must never collapse two
// distinct image files merely because their names differ by case.
func generatedImageRegistrationReplacements(source string) []imagePreviewRendererReplacement {
	marker := "/*" + imagePreviewPatchMarker + "*/"
	var replacements []imagePreviewRendererReplacement
	searchFrom := 0
	for {
		startOffset := strings.Index(source[searchFrom:], marker)
		if startOffset < 0 {
			break
		}
		start := searchFrom + startOffset
		endMatch := imagePreviewLegacyBlockEndPattern.FindStringIndex(source[start:])
		if endMatch == nil || endMatch[0] > 8*1024 {
			searchFrom = start + len(marker)
			continue
		}
		end := start + endMatch[0] + 1
		block := source[start:end]
		header := imagePreviewCurrentHeaderPattern.FindStringSubmatch(block)
		if header == nil || header[2] != header[3] || strings.Contains(block, "__antigravityWFRememberGeneratedImageV2") {
			searchFrom = start + len(marker)
			continue
		}
		media, image := header[1], header[4]
		if !strings.Contains(block, image+`=typeof `+image+`==="string"?`+image+`:void 0`) ||
			!strings.Contains(block, media+`?.uri`) {
			searchFrom = start + len(marker)
			continue
		}
		registration := generatedImageRegistrationRuntime(image, media+`?.uri`)
		replacements = append(replacements, imagePreviewRendererReplacement{start: end, end: end, value: registration})
		searchFrom = end
	}
	if len(replacements) == 0 {
		replacements = nativeGeneratedImageRegistrationReplacements(source)
	}
	return replacements
}

func nativeGeneratedImageRegistrationReplacements(source string) []imagePreviewRendererReplacement {
	marker := "/*" + imagePreviewNativeCompatibleMarker + "*/"
	var replacements []imagePreviewRendererReplacement
	for _, match := range findNativeImagePreviewRendererMatches(source) {
		if match.start < len(marker) || source[match.start-len(marker):match.start] != marker {
			continue
		}
		value := match.step + `.generatedMedia?.uri`
		replacements = append(replacements, imagePreviewRendererReplacement{
			start: match.prefixEnd, end: match.prefixEnd, value: generatedImageRegistrationRuntime(value),
		})
	}
	return replacements
}

func generatedImageRegistrationRuntime(values ...string) string {
	registration := generatedImageKeyDefinition() +
		`globalThis.__antigravityWFGeneratedImageTimesV2??=new Map,` +
		`globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageMountedDuplicateHiderDefinition() + generatedImageRememberDefinition() +
		generatedImageIsRecentDefinition() +
		generatedImageConsumeDefinition()
	for index, value := range values {
		if index > 0 {
			registration += ","
		}
		registration += value + `&&globalThis.__antigravityWFRememberGeneratedImageV2(` + value + `)`
	}
	return registration + ";"
}

func generatedImageKeyDefinition() string {
	return `globalThis.__antigravityWFImageKeyV2??=(value=>{let text=typeof value==="string"?value:"";if(!text)return"";try{text=decodeURIComponent(text)}catch{}return text.replace(/^vscode-file:\/\/(?:vscode-app)?/i,"").replace(/^file:\/\/(?:localhost)?/i,"").replace(/\\/g,"/")}),`
}

// The artifact component can execute in a different renderer global from the
// native generated-image card. Install the consumer beside that component as
// well; its shared queue contains no URI or image data.
func generatedImageConsumerRuntime() string {
	return generatedImageKeyDefinition() +
		`globalThis.__antigravityWFGeneratedImageEventsV4??=[],` +
		generatedImageConsumeDefinition()
}

func generatedImageLegacyRememberDefinition() string {
	return `globalThis.__antigravityWFRememberGeneratedImageV2??=(value=>{let key=globalThis.__antigravityWFImageKeyV2(value);if(!key)return;let now=Date.now(),images=globalThis.__antigravityWFGeneratedImageTimesV2;images instanceof Map||(images=globalThis.__antigravityWFGeneratedImageTimesV2=new Map),images.set(key,now);if(images.size>128)for(let[candidate,seen]of images)if(typeof seen!=="number"||now-seen>=600000||images.size>128)images.delete(candidate)}),`
}

func generatedImageMountedDuplicateHiderDefinition() string {
	return `globalThis.__antigravityWFHideGeneratedImageV2??=(key=>{if(!key||typeof document==="undefined"||typeof document.querySelectorAll!=="function")return;let hide=()=>{for(let image of document.querySelectorAll("img")){let imageKey=globalThis.__antigravityWFImageKeyV2(image.currentSrc||image.src);if(imageKey!==key)continue;let container=typeof image.closest==="function"?image.closest('[class~="group/media"]'):null;if(container){container.hidden=!0,container.style&&(container.style.display="none"),container.setAttribute?.("data-antigravity-wf-generated-duplicate","true")}}};hide(),typeof queueMicrotask==="function"&&queueMicrotask(hide),typeof setTimeout==="function"&&(setTimeout(hide,0),setTimeout(hide,120))}),`
}

func generatedImageRememberDefinition() string {
	// Assignment (rather than ??=) deliberately upgrades an already-loaded
	// function. localStorage carries only a numeric fingerprint and timestamp;
	// it never persists the user's local image path or generated bytes.
	return `globalThis.__antigravityWFRememberGeneratedImageV2=(value=>{let key=globalThis.__antigravityWFImageKeyV2(value);if(!key)return;let now=Date.now(),images=globalThis.__antigravityWFGeneratedImageTimesV2;images instanceof Map||(images=globalThis.__antigravityWFGeneratedImageTimesV2=new Map);let seen=images.get(key);images.set(key,now);if(images.size>128)for(let[candidate,time]of images)if(typeof time!=="number"||now-time>=600000||images.size>128)images.delete(candidate);let fresh=!(typeof seen==="number"&&now-seen<600000),events=globalThis.__antigravityWFGeneratedImageEventsV4;Array.isArray(events)||(events=globalThis.__antigravityWFGeneratedImageEventsV4=[]),events.splice(0,events.length,...events.filter(event=>event&&typeof event.time==="number"&&now-event.time<600000));if(fresh){events.push({key,time:now,consumed:!1});try{let hash=0;for(let index=0;index<key.length;index++)hash=(Math.imul(hash,31)+key.charCodeAt(index))>>>0;let storage=globalThis.localStorage,storageKey="antigravity-wf-generated-image-events-v5",shared=JSON.parse(storage?.getItem(storageKey)||"[]");Array.isArray(shared)||(shared=[]),shared=shared.filter(event=>event&&typeof event.time==="number"&&now-event.time<600000).slice(-15),shared.some(event=>!event.consumed&&event.hash===hash)||(shared.push({hash,time:now,consumed:!1}),storage?.setItem(storageKey,JSON.stringify(shared)))}catch{}}globalThis.__antigravityWFHideGeneratedImageV2?.(key)}),`
}

func generatedImageConsumeDefinition() string {
	return `globalThis.__antigravityWFConsumeGeneratedImageV4=((...values)=>{let now=Date.now(),keys=values.map(value=>globalThis.__antigravityWFImageKeyV2(value)).filter(Boolean),events=globalThis.__antigravityWFGeneratedImageEventsV4;Array.isArray(events)||(events=globalThis.__antigravityWFGeneratedImageEventsV4=[]),events.splice(0,events.length,...events.filter(event=>event&&typeof event.time==="number"&&now-event.time<600000));let event=events.find(candidate=>!candidate.consumed&&keys.includes(candidate.key));event||(event=events.find(candidate=>!candidate.consumed));let consumed=!!event;event&&(event.consumed=!0);try{let hashes=keys.map(key=>{let hash=0;for(let index=0;index<key.length;index++)hash=(Math.imul(hash,31)+key.charCodeAt(index))>>>0;return hash}),storage=globalThis.localStorage,storageKey="antigravity-wf-generated-image-events-v5",shared=JSON.parse(storage?.getItem(storageKey)||"[]");Array.isArray(shared)||(shared=[]),shared=shared.filter(candidate=>candidate&&typeof candidate.time==="number"&&now-candidate.time<600000);let sharedEvent=shared.find(candidate=>!candidate.consumed&&hashes.includes(candidate.hash));sharedEvent||(sharedEvent=shared.find(candidate=>!candidate.consumed)),sharedEvent&&(sharedEvent.consumed=!0,consumed=!0),storage?.setItem(storageKey,JSON.stringify(shared))}catch{}return consumed}),`
}

func generatedImageV4RememberDefinition() string {
	return `globalThis.__antigravityWFRememberGeneratedImageV2=(value=>{let key=globalThis.__antigravityWFImageKeyV2(value);if(!key)return;let now=Date.now(),images=globalThis.__antigravityWFGeneratedImageTimesV2;images instanceof Map||(images=globalThis.__antigravityWFGeneratedImageTimesV2=new Map);let seen=images.get(key);images.set(key,now);if(images.size>128)for(let[candidate,time]of images)if(typeof time!=="number"||now-time>=600000||images.size>128)images.delete(candidate);let events=globalThis.__antigravityWFGeneratedImageEventsV4;Array.isArray(events)||(events=globalThis.__antigravityWFGeneratedImageEventsV4=[]),events.splice(0,events.length,...events.filter(event=>event&&typeof event.time==="number"&&now-event.time<120000));if(!(typeof seen==="number"&&now-seen<5000))events.push({key,time:now,consumed:!1});globalThis.__antigravityWFHideGeneratedImageV2?.(key)}),`
}

func generatedImageV4ConsumeDefinition() string {
	return `globalThis.__antigravityWFConsumeGeneratedImageV4??=((...values)=>{let now=Date.now(),keys=values.map(value=>globalThis.__antigravityWFImageKeyV2(value)).filter(Boolean),events=globalThis.__antigravityWFGeneratedImageEventsV4;Array.isArray(events)||(events=globalThis.__antigravityWFGeneratedImageEventsV4=[]),events.splice(0,events.length,...events.filter(event=>event&&typeof event.time==="number"&&now-event.time<120000));let event=events.find(candidate=>!candidate.consumed&&keys.includes(candidate.key));event||(event=events.find(candidate=>!candidate.consumed));if(!event)return!1;return event.consumed=!0,!0}),`
}

func generatedImageIsRecentDefinition() string {
	return `globalThis.__antigravityWFIsRecentGeneratedImageV2??=(value=>{let key=globalThis.__antigravityWFImageKeyV2(value),images=globalThis.__antigravityWFGeneratedImageTimesV2;if(!key||!(images instanceof Map))return!1;let now=Date.now(),seen=images.get(key);return typeof seen==="number"&&now>=seen&&now-seen<600000||(images.delete(key),!1)}),`
}

func generatedImageV3MountedDuplicateHiderDefinition() string {
	return `globalThis.__antigravityWFHideGeneratedImageV2??=(key=>{if(!key||typeof document==="undefined"||typeof document.querySelectorAll!=="function")return;let hide=()=>{for(let image of document.querySelectorAll("img")){let imageKey=globalThis.__antigravityWFImageKeyV2(image.currentSrc||image.src);if(imageKey!==key)continue;let container=typeof image.closest==="function"?image.closest('[class~="group/media"]'):null;if(container){container.hidden=!0,container.style&&(container.style.display="none"),container.setAttribute?.("data-antigravity-wf-generated-duplicate","true")}}};hide(),typeof queueMicrotask==="function"&&queueMicrotask(hide),typeof setTimeout==="function"&&(setTimeout(hide,0),setTimeout(hide,120))}),`
}

func generatedImageV3RememberDefinition() string {
	return `globalThis.__antigravityWFRememberGeneratedImageV2=(value=>{let key=globalThis.__antigravityWFImageKeyV2(value);if(!key)return;let now=Date.now(),images=globalThis.__antigravityWFGeneratedImageTimesV2;images instanceof Map||(images=globalThis.__antigravityWFGeneratedImageTimesV2=new Map),images.set(key,now);if(images.size>128)for(let[candidate,seen]of images)if(typeof seen!=="number"||now-seen>=600000||images.size>128)images.delete(candidate);globalThis.__antigravityWFHideGeneratedImageV2?.(key)}),`
}

func imageArtifactMarkdownReplacement(source string) *imagePreviewRendererReplacement {
	matches := imageArtifactMarkdownRendererPrefixPattern.FindAllStringSubmatchIndex(source, -1)
	candidates := make([]imagePreviewRendererReplacement, 0, 1)
	for _, match := range matches {
		sourceValue := imagePreviewSubmatch(source, match, 2)
		altValue := imagePreviewSubmatch(source, match, 3)
		originalPath := imagePreviewSubmatch(source, match, 4)
		errorState := imagePreviewSubmatch(source, match, 8)
		resolvedValue := imagePreviewSubmatch(source, match, 11)
		if sourceValue == "" || altValue == "" || originalPath == "" || errorState == "" || resolvedValue == "" || sourceValue != imagePreviewSubmatch(source, match, 13) {
			continue
		}
		end, ok := imageArtifactMarkdownRendererEnd(source, match[0], match[1])
		if !ok || end-match[0] > 16*1024 {
			continue
		}
		component := source[match[0]:end]
		// Antigravity 2.5.5 places image, audio, and video components next to
		// each other with the same prop/state prefix. Select only the exact image
		// component instead of rejecting the bundle because all three match.
		if !strings.Contains(component, `alt:`+altValue+`||"Artifact image"`) ||
			strings.Contains(component, `("audio",{`) || strings.Contains(component, `("video",{`) {
			continue
		}
		returnNeedle := `;return!` + sourceValue + `||` + errorState + `?`
		ifNeedle := `;if(!` + sourceValue + `||` + errorState + `)return`
		returnCount := strings.Count(component, returnNeedle)
		ifCount := strings.Count(component, ifNeedle)
		if returnCount+ifCount != 1 {
			continue
		}
		needle := returnNeedle
		if ifCount == 1 {
			needle = ifNeedle
		}
		returnOffset := strings.Index(component[match[1]-match[0]:], needle)
		if returnOffset < 0 {
			continue
		}
		returnStart := match[1] + returnOffset
		duplicate := `$wfImageDuplicate=(` + generatedImageConsumerRuntime() + `!!globalThis.__antigravityWFConsumeGeneratedImageV4&&globalThis.__antigravityWFConsumeGeneratedImageV4(` + sourceValue + `,` + resolvedValue + `,` + originalPath + `)),`
		beforeReturn := source[match[0]:match[1]] + duplicate + source[match[1]:returnStart]
		afterReturn := source[returnStart:end]
		if returnCount == 1 {
			afterReturn = strings.Replace(afterReturn, returnNeedle, `;return $wfImageDuplicate?null:!`+sourceValue+`||`+errorState+`?`, 1)
		} else {
			afterReturn = strings.Replace(afterReturn, ifNeedle, `;if($wfImageDuplicate)return null;if(!`+sourceValue+`||`+errorState+`)return`, 1)
		}
		patchedComponent := beforeReturn + afterReturn
		thumbnailNeedle := `alt:` + altValue + `||"Artifact image",`
		if strings.Count(patchedComponent, thumbnailNeedle) != 1 {
			continue
		}
		patchedComponent = strings.Replace(patchedComponent, thumbnailNeedle, thumbnailNeedle+imageArtifactThumbnailStyle, 1)
		candidates = append(candidates, imagePreviewRendererReplacement{
			start: match[0], end: end,
			value: "/*" + imageGenerationDedupePatchMarker + "*/" + patchedComponent,
		})
	}
	if len(candidates) != 1 {
		return nil
	}
	return &candidates[0]
}

// imageArtifactMarkdownRendererEnd finds the end of the complete arrow
// component rather than replacing an arbitrary 4 KiB suffix after a matching
// prefix. Quoted strings and comments are skipped; if the JavaScript shape is
// not balanced exactly as expected, the compatibility patch simply declines
// to modify it.
func imageArtifactMarkdownRendererEnd(source string, start, prefixEnd int) (int, bool) {
	if start < 0 || prefixEnd < start || prefixEnd > len(source) {
		return 0, false
	}
	openOffset := strings.LastIndex(source[start:prefixEnd], "=>{")
	if openOffset < 0 {
		return 0, false
	}
	end, ok := imagePreviewJavaScriptBalancedBlockEnd(source, start+openOffset+2)
	if !ok {
		return 0, false
	}
	if end < len(source) && source[end] == ';' {
		end++
	}
	return end, true
}

func imagePreviewJavaScriptBalancedBlockEnd(source string, open int) (int, bool) {
	if open < 0 || open >= len(source) || source[open] != '{' {
		return 0, false
	}
	depth := 0
	var quote byte
	for index := open; index < len(source); index++ {
		character := source[index]
		if quote != 0 {
			if character == '\\' {
				index++
				continue
			}
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '/' && index+1 < len(source) {
			switch source[index+1] {
			case '/':
				index += 2
				for index < len(source) && source[index] != '\n' && source[index] != '\r' {
					index++
				}
				continue
			case '*':
				close := strings.Index(source[index+2:], "*/")
				if close < 0 {
					return 0, false
				}
				index += close + 3
				continue
			}
		}
		switch character {
		case '\'', '"', '`':
			quote = character
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return index + 1, true
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}

func upgradeLegacyImagePreviewRenderers(source string, recognized bool) (string, bool, bool) {
	var output strings.Builder
	cursor, searchFrom := 0, 0
	changed := false
	for {
		start, marker := nextLegacyImagePreviewMarker(source, searchFrom)
		if start < 0 {
			break
		}
		endMatch := imagePreviewLegacyBlockEndPattern.FindStringIndex(source[start:])
		if endMatch == nil || endMatch[0] > 8*1024 {
			searchFrom = start + len(marker)
			continue
		}
		end := start + endMatch[0] // points at the old block's final semicolon.
		replacement, ok := upgradeLegacyImagePreviewBlock(source[start:end])
		if !ok {
			searchFrom = start + len(marker)
			continue
		}
		output.WriteString(source[cursor:start])
		output.WriteString(replacement)
		// replacement includes the semicolon. Skip the original semicolon and
		// preserve the following `let` declaration verbatim.
		cursor = end + 1
		searchFrom = cursor
		recognized = true
		changed = true
	}
	if !changed {
		return source, recognized, false
	}
	output.WriteString(source[cursor:])
	return output.String(), recognized, true
}

func nextLegacyImagePreviewMarker(source string, from int) (int, string) {
	bestStart, bestMarker := -1, ""
	for _, marker := range []string{imagePreviewPatchV2Marker, imagePreviewPatchV3Marker, imagePreviewPatchV4Marker, imagePreviewPatchV5Marker, imagePreviewPatchV6Marker, imagePreviewPatchV7Marker} {
		index := strings.Index(source[from:], "/*"+marker+"*/")
		if index >= 0 && (bestStart < 0 || index < bestStart) {
			bestStart, bestMarker = index, marker
		}
	}
	if bestStart < 0 {
		return -1, ""
	}
	return from + bestStart, bestMarker
}

func upgradeLegacyImagePreviewBlock(block string) (string, bool) {
	header := imagePreviewLegacyHeaderPattern.FindStringSubmatch(block)
	if header == nil || header[3] != header[4] {
		return "", false
	}
	media, step, image := header[2], header[3], header[5]
	resolver := imagePreviewLegacyResolverPattern.FindStringSubmatch(block)
	inline := imagePreviewLegacyInlinePattern.FindStringSubmatch(block)
	if resolver == nil || inline == nil ||
		resolver[1] != media || resolver[2] != image || resolver[4] != media ||
		inline[1] != media || inline[2] != image || inline[3] != media ||
		!strings.Contains(block, "!"+image+"&&"+media+"?.base64Data") {
		return "", false
	}
	if (header[1] == "v3" || header[1] == "v4" || header[1] == "v5") && (!strings.Contains(block, `startsWith("file://")`) || !strings.Contains(block, "decodeURIComponent")) {
		return "", false
	}
	if (header[1] == "v5" || header[1] == "v6" || header[1] == "v7") && !strings.Contains(block, `typeof `+image+`==="string"`) {
		return "", false
	}
	return imagePreviewV4Renderer(media, step, image, resolver[3], inline[4]), true
}

type imagePreviewRendererReplacement struct {
	start int
	end   int
	value string
}

type imageGenerationTitleRendererMatch struct {
	start         int
	end           int
	component     string
	step          string
	status        string
	resolvedModel string
	displayName   string
	isNewModel    string
}

func patchImageGenerationUIRenderers(source string) (string, bool, bool) {
	recognized := strings.Contains(source, imageGenerationUIPatchMarker)
	changed := false
	updated, legacyRecognized, legacyChanged := upgradeLegacyImageGenerationUIRenderers(source)
	source = updated
	recognized = recognized || legacyRecognized
	changed = changed || legacyChanged

	// Do not let an already-patched v3 split title component mask a separate
	// unpatched IDE 2.1 combined component in the same renderer bundle.
	updated, combinedRecognized, combinedChanged := patchCombinedImageGenerationUIRenderers(source)
	source = updated
	recognized = recognized || combinedRecognized
	changed = changed || combinedChanged

	titleMatches := findImageGenerationTitleRendererMatches(source)
	resultMatches := imageGenerationResultRendererPattern.FindAllStringSubmatchIndex(source, -1)
	if len(titleMatches) == 0 || len(resultMatches) == 0 {
		return source, recognized, changed
	}

	titlesByComponent := make(map[string][]imageGenerationTitleRendererMatch, len(titleMatches))
	for _, titleMatch := range titleMatches {
		titlesByComponent[titleMatch.component] = append(titlesByComponent[titleMatch.component], titleMatch)
	}

	replacements := make([]imagePreviewRendererReplacement, 0, len(resultMatches)*2)
	usedTitleOffsets := map[int]bool{}
	for _, resultMatch := range resultMatches {
		if !validImageGenerationResultRendererMatch(source, resultMatch) {
			continue
		}
		titleComponent := imagePreviewSubmatch(source, resultMatch, 10)
		var titleMatch imageGenerationTitleRendererMatch
		for _, candidate := range titlesByComponent[titleComponent] {
			if candidate.end <= resultMatch[0] && resultMatch[0]-candidate.end <= 8*1024 && !usedTitleOffsets[candidate.start] {
				titleMatch = candidate
				break
			}
		}
		if titleMatch.component == "" {
			continue
		}

		hookSearchEnd := resultMatch[1] + 4*1024
		if hookSearchEnd > len(source) {
			hookSearchEnd = len(source)
		}
		hookMatch := imageGenerationExpansionHooksPattern.FindStringSubmatchIndex(source[resultMatch[1]:hookSearchEnd])
		if hookMatch == nil || !validImageGenerationExpansionHooks(source[resultMatch[1]:hookSearchEnd], hookMatch) {
			continue
		}
		titleReplacement, ok := imageGenerationTitleRendererReplacement(source, titleMatch)
		if !ok {
			continue
		}
		resultReplacement := imageGenerationResultRendererReplacement(source, resultMatch, source[resultMatch[1]:hookSearchEnd], hookMatch)
		replacements = append(replacements,
			imagePreviewRendererReplacement{start: titleMatch.start, end: titleMatch.end, value: titleReplacement},
			imagePreviewRendererReplacement{start: resultMatch[0], end: resultMatch[1], value: resultReplacement},
		)
		usedTitleOffsets[titleMatch.start] = true
	}
	if len(replacements) == 0 {
		return source, recognized, changed
	}

	sort.Slice(replacements, func(left, right int) bool {
		return replacements[left].start < replacements[right].start
	})
	var output strings.Builder
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			return source, recognized, changed
		}
		output.WriteString(source[last:replacement.start])
		output.WriteString(replacement.value)
		last = replacement.end
	}
	output.WriteString(source[last:])
	return output.String(), true, true
}

func patchCombinedImageGenerationUIRenderers(source string) (string, bool, bool) {
	replacements := make([]imagePreviewRendererReplacement, 0)
	for _, pattern := range imageGenerationCombinedRendererPatterns {
		for _, match := range pattern.expression.FindAllStringSubmatchIndex(source, -1) {
			// A marker directly before the component is an exact marker emitted
			// by this function. Other v3 components must not prevent this scan.
			if imageGenerationUIPatchMarkerImmediatelyBefore(source, match[0]) {
				continue
			}
			replacement, ok := imageGenerationCombinedRendererReplacement(source, match, pattern)
			if !ok {
				continue
			}
			replacements = append(replacements, replacement)
		}
	}
	if len(replacements) == 0 {
		return source, false, false
	}
	sort.Slice(replacements, func(left, right int) bool {
		return replacements[left].start < replacements[right].start
	})
	var output strings.Builder
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			// Overlapping rich/simple matches are an unknown layout, not an
			// invitation to choose one arbitrarily.
			return source, false, false
		}
		output.WriteString(source[last:replacement.start])
		output.WriteString(replacement.value)
		last = replacement.end
	}
	output.WriteString(source[last:])
	return output.String(), true, true
}

func imageGenerationUIPatchMarkerImmediatelyBefore(source string, offset int) bool {
	marker := "/*" + imageGenerationUIPatchMarker + "*/"
	return offset >= len(marker) && source[offset-len(marker):offset] == marker
}

func imageGenerationCombinedRendererReplacement(source string, match []int, pattern imageGenerationCombinedRendererPattern) (imagePreviewRendererReplacement, bool) {
	step := imagePreviewSubmatch(source, match, pattern.stepGroup)
	status := imagePreviewSubmatch(source, match, pattern.statusGroup)
	hasMedia := imagePreviewSubmatch(source, match, pattern.hasMediaGroup)
	resolvedModel := imagePreviewSubmatch(source, match, pattern.resolvedModelGroup)
	displayName := imagePreviewSubmatch(source, match, pattern.displayNameGroup)
	isNewModel := imagePreviewSubmatch(source, match, pattern.isNewModelGroup)
	title := imagePreviewSubmatch(source, match, pattern.titleGroup)
	if step == "" || status == "" || hasMedia == "" || resolvedModel == "" || displayName == "" || isNewModel == "" || title == "" {
		return imagePreviewRendererReplacement{}, false
	}
	stepReferences := make([]string, 0, len(pattern.stepReferenceGroups)+1)
	stepReferences = append(stepReferences, step)
	for _, group := range pattern.stepReferenceGroups {
		stepReferences = append(stepReferences, imagePreviewSubmatch(source, match, group))
	}
	if !sameImagePreviewIdentifiers(stepReferences...) {
		return imagePreviewRendererReplacement{}, false
	}
	modelReferences := make([]string, 0, len(pattern.resolvedModelRefGroups)+1)
	modelReferences = append(modelReferences, resolvedModel)
	for _, group := range pattern.resolvedModelRefGroups {
		modelReferences = append(modelReferences, imagePreviewSubmatch(source, match, group))
	}
	if !sameImagePreviewIdentifiers(modelReferences...) {
		return imagePreviewRendererReplacement{}, false
	}

	openOffset := strings.LastIndex(source[match[0]:match[1]], "=>{")
	if openOffset < 0 {
		return imagePreviewRendererReplacement{}, false
	}
	componentEnd, ok := imagePreviewJavaScriptBalancedBlockEnd(source, match[0]+openOffset+2)
	if !ok || componentEnd-match[0] > 8*1024 {
		return imagePreviewRendererReplacement{}, false
	}
	endOffset := strings.Index(source[match[1]:componentEnd], ";return ")
	if endOffset < 0 || endOffset > 2*1024 {
		return imagePreviewRendererReplacement{}, false
	}
	end := match[1] + endOffset
	current := source[match[0]:end]
	oldModelLabel := displayName + `=` + resolvedModel + `?.displayName||"Gemini"`
	if strings.Count(current, oldModelLabel) != 1 {
		return imagePreviewRendererReplacement{}, false
	}
	updated := strings.Replace(current, oldModelLabel, imageGenerationModelLabel(step, resolvedModel, displayName), 1)
	oldIsNewModel := `,` + isNewModel + `=` + resolvedModel + `?.isNewModel??!1,` + title + `=`
	newIsNewModel := `,` + isNewModel + `=` + resolvedModel + `?.isNewModel??!1,$wfIsGeminiImage=!!` + resolvedModel + `||/^gemini[-_]/i.test(` + step + `.modelName||""),` + title + `=`
	if strings.Count(updated, oldIsNewModel) != 1 {
		return imagePreviewRendererReplacement{}, false
	}
	updated = strings.Replace(updated, oldIsNewModel, newIsNewModel, 1)
	if strings.Count(updated, ":"+hasMedia+"?`Generated with ") != 1 {
		return imagePreviewRendererReplacement{}, false
	}
	for _, prefix := range []string{"Generating with ", "Generated with ", "Generate with "} {
		oldTitle := "`" + prefix + "${" + displayName + "} \\u{1F34C}`"
		newTitle := "`" + prefix + "${" + displayName + "}${$wfIsGeminiImage?\" \\u{1F34C}\":\"\"}`"
		if strings.Count(updated, oldTitle) != 1 {
			return imagePreviewRendererReplacement{}, false
		}
		updated = strings.Replace(updated, oldTitle, newTitle, 1)
	}
	titleAssignment := title + `=`
	if strings.Count(updated, titleAssignment) != 1 {
		return imagePreviewRendererReplacement{}, false
	}
	titleOffset := strings.Index(updated, titleAssignment)
	titleExpression := updated[titleOffset+len(titleAssignment):]
	loadingPattern := regexp.MustCompile(`^(` + imagePreviewJavaScriptIdentifier + `)\(` + regexp.QuoteMeta(status) + `\)\?`)
	loadingMatch := loadingPattern.FindStringSubmatch(titleExpression)
	if loadingMatch == nil {
		return imagePreviewRendererReplacement{}, false
	}
	neutralTitle := loadingMatch[1] + `(` + status + `)&&!` + step + `.modelName?` + "`Generating image`:" + titleExpression
	updated = updated[:titleOffset+len(titleAssignment)] + neutralTitle
	return imagePreviewRendererReplacement{
		start: match[0],
		end:   end,
		value: "/*" + imageGenerationUIPatchMarker + "*/" + updated,
	}, true
}

func imagePreviewSubmatch(source string, match []int, group int) string {
	if group < 0 || group*2+1 >= len(match) {
		return ""
	}
	start, end := match[group*2], match[group*2+1]
	if start < 0 || end < start {
		return ""
	}
	return source[start:end]
}

func findImageGenerationTitleRendererMatches(source string) []imageGenerationTitleRendererMatch {
	matches := make([]imageGenerationTitleRendererMatch, 0)
	for _, pattern := range imageGenerationTitleRendererPatterns {
		for _, match := range pattern.expression.FindAllStringSubmatchIndex(source, -1) {
			titleMatch, ok := imageGenerationTitleRendererMatchFromPattern(source, match, pattern)
			if ok {
				matches = append(matches, titleMatch)
			}
		}
	}
	sort.Slice(matches, func(left, right int) bool {
		return matches[left].start < matches[right].start
	})
	return matches
}

func imageGenerationTitleRendererMatchFromPattern(source string, match []int, pattern imageGenerationTitleRendererPattern) (imageGenerationTitleRendererMatch, bool) {
	step := imagePreviewSubmatch(source, match, pattern.stepGroup)
	stepReferences := make([]string, 0, len(pattern.stepReferenceGroups)+1)
	stepReferences = append(stepReferences, step)
	for _, group := range pattern.stepReferenceGroups {
		stepReferences = append(stepReferences, imagePreviewSubmatch(source, match, group))
	}
	if !sameImagePreviewIdentifiers(stepReferences...) {
		return imageGenerationTitleRendererMatch{}, false
	}

	resolvedModel := imagePreviewSubmatch(source, match, pattern.resolvedModelGroup)
	resolvedModelReferences := make([]string, 0, len(pattern.resolvedModelRefGroups)+1)
	resolvedModelReferences = append(resolvedModelReferences, resolvedModel)
	for _, group := range pattern.resolvedModelRefGroups {
		resolvedModelReferences = append(resolvedModelReferences, imagePreviewSubmatch(source, match, group))
	}
	if !sameImagePreviewIdentifiers(resolvedModelReferences...) {
		return imageGenerationTitleRendererMatch{}, false
	}

	end, ok := imageGenerationTitleRendererEnd(source, match)
	if !ok {
		return imageGenerationTitleRendererMatch{}, false
	}
	component := imagePreviewSubmatch(source, match, pattern.componentGroup)
	status := imagePreviewSubmatch(source, match, pattern.statusGroup)
	displayName := imagePreviewSubmatch(source, match, pattern.displayNameGroup)
	isNewModel := imagePreviewSubmatch(source, match, pattern.isNewModelGroup)
	if component == "" || status == "" || displayName == "" || isNewModel == "" {
		return imageGenerationTitleRendererMatch{}, false
	}
	return imageGenerationTitleRendererMatch{
		start:         match[0],
		end:           end,
		component:     component,
		step:          step,
		status:        status,
		resolvedModel: resolvedModel,
		displayName:   displayName,
		isNewModel:    isNewModel,
	}, true
}

func imageGenerationTitleRendererEnd(source string, match []int) (int, bool) {
	if len(match) < 2 || match[1] < 0 {
		return 0, false
	}
	componentEndOffset := strings.Index(source[match[1]:], "})},")
	if componentEndOffset < 0 || componentEndOffset > 4*1024 {
		return 0, false
	}
	return match[1] + componentEndOffset + len("})}"), true
}

func validImageGenerationResultRendererMatch(source string, match []int) bool {
	return sameImagePreviewIdentifiers(
		imagePreviewSubmatch(source, match, 2),
		imagePreviewSubmatch(source, match, 11),
		imagePreviewSubmatch(source, match, 16),
	) && sameImagePreviewIdentifiers(
		imagePreviewSubmatch(source, match, 3),
		imagePreviewSubmatch(source, match, 8),
		imagePreviewSubmatch(source, match, 12),
		imagePreviewSubmatch(source, match, 17),
	) && sameImagePreviewIdentifiers(
		imagePreviewSubmatch(source, match, 4),
		imagePreviewSubmatch(source, match, 13),
	) && sameImagePreviewIdentifiers(
		imagePreviewSubmatch(source, match, 5),
		imagePreviewSubmatch(source, match, 9),
		imagePreviewSubmatch(source, match, 14),
	)
}

func validImageGenerationExpansionHooks(source string, match []int) bool {
	return imagePreviewSubmatch(source, match, 2) == imagePreviewSubmatch(source, match, 6) &&
		imagePreviewSubmatch(source, match, 7) == imagePreviewSubmatch(source, match, 8)
}

func sameImagePreviewIdentifiers(values ...string) bool {
	if len(values) == 0 || values[0] == "" {
		return false
	}
	for _, value := range values[1:] {
		if value != values[0] {
			return false
		}
	}
	return true
}

func imageGenerationTitleRendererReplacement(source string, match imageGenerationTitleRendererMatch) (string, bool) {
	if match.start < 0 || match.end < match.start || match.end > len(source) || match.step == "" || match.resolvedModel == "" || match.displayName == "" || match.isNewModel == "" {
		return "", false
	}
	current := source[match.start:match.end]
	step, resolvedModel, displayName := match.step, match.resolvedModel, match.displayName

	oldModelLabel := displayName + `=` + resolvedModel + `?.displayName||"Gemini"`
	newModelLabel := imageGenerationModelLabel(step, resolvedModel, displayName)
	if strings.Count(current, oldModelLabel) != 1 {
		return "", false
	}
	updated := strings.Replace(current, oldModelLabel, newModelLabel, 1)
	oldIsNewModel := `,` + match.isNewModel + `=` + resolvedModel + `?.isNewModel??!1;return `
	newIsNewModel := `,` + match.isNewModel + `=` + resolvedModel + `?.isNewModel??!1,$wfIsGeminiImage=!!` + resolvedModel + `||/^gemini[-_]/i.test(` + step + `.modelName||"");return `
	if strings.Count(updated, oldIsNewModel) != 1 {
		return "", false
	}
	updated = strings.Replace(updated, oldIsNewModel, newIsNewModel, 1)
	for _, prefix := range []string{"Generating with ", "Generated with ", "Generate with "} {
		oldTitle := "`" + prefix + "${" + displayName + "} \\u{1F34C}`"
		newTitle := "`" + prefix + "${" + displayName + "}${$wfIsGeminiImage?\" \\u{1F34C}\":\"\"}`"
		if strings.Count(updated, oldTitle) != 1 {
			return "", false
		}
		updated = strings.Replace(updated, oldTitle, newTitle, 1)
	}
	return addNeutralImageGenerationLoadingTitle(updated, step, match.status)
}

func imageGenerationModelLabel(step, resolvedModel, displayName string) string {
	return displayName + `=` + resolvedModel + `?.displayName||((modelName)=>{let match=/^gpt-image-(\d+)$/i.exec(modelName||"");return match?"GPT Image "+match[1]:void 0})(` + step + `.modelName)||((modelName)=>{let match=/^gemini[-_](.+)$/i.exec(modelName||"");return match?"Gemini "+match[1].replace(/[-_]+/g," ").replace(/\b\w/g,character=>character.toUpperCase()):void 0})(` + step + `.modelName)||` + step + `.modelName||"Image generator"`
}

func addNeutralImageGenerationLoadingTitle(source, step, status string) (string, bool) {
	matches := imageGenerationTitleChildrenPattern.FindAllStringSubmatchIndex(source, -1)
	for _, match := range matches {
		if imagePreviewSubmatch(source, match, 3) != status {
			continue
		}
		newTitle := imagePreviewSubmatch(source, match, 1) + `("span",{children:` + imagePreviewSubmatch(source, match, 2) + `(` + status + `)&&!` + step + `.modelName?` + "`Generating image`:" + imagePreviewSubmatch(source, match, 2) + `(` + status + `)?`
		return source[:match[0]] + newTitle + source[match[1]:], true
	}
	return source, false
}

func upgradeLegacyImageGenerationUIRenderers(source string) (string, bool, bool) {
	recognized := false
	changed := false
	if strings.Contains(source, imageGenerationUIPatchV2Marker) {
		// v2 was emitted only by this helper. It already has the expanded panel
		// and title behaviour; migrate its version marker so a bundle can carry
		// v2 and newly recognised stock components without an early-return gap.
		source = strings.ReplaceAll(source, imageGenerationUIPatchV2Marker, imageGenerationUIPatchMarker)
		recognized = true
		changed = true
	}
	titleMatches := imageGenerationLegacyUITitleRendererPattern.FindAllStringSubmatchIndex(source, -1)
	if len(titleMatches) == 0 {
		return source, recognized, changed
	}
	replacements := make([]imagePreviewRendererReplacement, 0, len(titleMatches)*2)
	usedMarkers := make(map[int]bool)
	for _, match := range titleMatches {
		if !sameImagePreviewIdentifiers(
			imagePreviewSubmatch(source, match, 2),
			imagePreviewSubmatch(source, match, 5),
			imagePreviewSubmatch(source, match, 7),
			imagePreviewSubmatch(source, match, 9),
			imagePreviewSubmatch(source, match, 12),
			imagePreviewSubmatch(source, match, 13),
		) || !sameImagePreviewIdentifiers(
			imagePreviewSubmatch(source, match, 6),
			imagePreviewSubmatch(source, match, 11),
			imagePreviewSubmatch(source, match, 15),
		) {
			continue
		}
		endOffset := strings.Index(source[match[1]:], "})},")
		if endOffset < 0 || endOffset > 4*1024 {
			continue
		}
		end := match[1] + endOffset + len("})}")
		markerEnd := end + 8*1024
		if markerEnd > len(source) {
			markerEnd = len(source)
		}
		markerOffset := strings.Index(source[end:markerEnd], "/*"+imageGenerationUIPatchV1Marker+"*/")
		if markerOffset < 0 {
			continue
		}
		markerStart := end + markerOffset
		if usedMarkers[markerStart] {
			continue
		}
		updatedTitle, ok := upgradeLegacyImageGenerationTitleRenderer(source[match[0]:end], source, match)
		if !ok {
			continue
		}
		replacements = append(replacements,
			imagePreviewRendererReplacement{start: match[0], end: end, value: updatedTitle},
			imagePreviewRendererReplacement{start: markerStart, end: markerStart + len("/*"+imageGenerationUIPatchV1Marker+"*/"), value: "/*" + imageGenerationUIPatchMarker + "*/"},
		)
		usedMarkers[markerStart] = true
	}
	if len(replacements) == 0 {
		return source, recognized, changed
	}
	sort.Slice(replacements, func(left, right int) bool {
		return replacements[left].start < replacements[right].start
	})
	var output strings.Builder
	last := 0
	for _, replacement := range replacements {
		if replacement.start < last {
			return source, recognized, changed
		}
		output.WriteString(source[last:replacement.start])
		output.WriteString(replacement.value)
		last = replacement.end
	}
	output.WriteString(source[last:])
	return output.String(), true, true
}

func upgradeLegacyImageGenerationTitleRenderer(current, source string, match []int) (string, bool) {
	step := imagePreviewSubmatch(source, match, 2)
	status := imagePreviewSubmatch(source, match, 3)
	resolvedModel := imagePreviewSubmatch(source, match, 6)
	displayName := imagePreviewSubmatch(source, match, 10)
	isNewModel := imagePreviewSubmatch(source, match, 14)
	oldModelLabel := displayName + `=` + resolvedModel + `?.displayName||(` + step + `.modelName?.replace(/^gpt-image-(\d+)$/i,"GPT Image $1"))||` + step + `.modelName||"Image generator"`
	if strings.Count(current, oldModelLabel) != 1 {
		return "", false
	}
	updated := strings.Replace(current, oldModelLabel, imageGenerationModelLabel(step, resolvedModel, displayName), 1)
	oldIsNewModel := `,` + isNewModel + `=` + resolvedModel + `?.isNewModel??!1;return `
	newIsNewModel := `,` + isNewModel + `=` + resolvedModel + `?.isNewModel??!1,$wfIsGeminiImage=!!` + resolvedModel + `||/^gemini[-_]/i.test(` + step + `.modelName||"");return `
	if strings.Count(updated, oldIsNewModel) != 1 {
		return "", false
	}
	updated = strings.Replace(updated, oldIsNewModel, newIsNewModel, 1)
	for _, prefix := range []string{"Generating with ", "Generated with ", "Generate with "} {
		oldTitle := "`" + prefix + "${" + displayName + "}${" + resolvedModel + `?" \u{1F34C}":""}` + "`"
		newTitle := "`" + prefix + "${" + displayName + "}${$wfIsGeminiImage?\" \\u{1F34C}\":\"\"}`"
		if strings.Count(updated, oldTitle) != 1 {
			return "", false
		}
		updated = strings.Replace(updated, oldTitle, newTitle, 1)
	}
	return addNeutralImageGenerationLoadingTitle(updated, step, status)
}

func imageGenerationResultRendererReplacement(source string, match []int, hookSource string, hookMatch []int) string {
	component := imagePreviewSubmatch(source, match, 1)
	step := imagePreviewSubmatch(source, match, 2)
	status := imagePreviewSubmatch(source, match, 3)
	err := imagePreviewSubmatch(source, match, 4)
	jsx := imagePreviewSubmatch(source, match, 5)
	container := imagePreviewSubmatch(source, match, 6)
	loading := imagePreviewSubmatch(source, match, 7)
	title := imagePreviewSubmatch(source, match, 10)
	supplementaryView := imagePreviewSubmatch(source, match, 15)
	stateHook := imagePreviewSubmatch(hookSource, hookMatch, 3)
	callbackHook := imagePreviewSubmatch(hookSource, hookMatch, 5)

	return "/*" + imageGenerationUIPatchMarker + "*/" + component + "=({step:" + step + ",status:" + status + ",error:" + err + "})=>{let[$wfImageExpanded,$wfSetImageExpanded]=" + stateHook + "(!0),$wfToggleImageExpanded=" + callbackHook + "(()=>{$wfSetImageExpanded(value=>!value)},[]);return " + jsx + "(" + container + ",{loading:" + loading + "(" + status + "),title:" + jsx + "(" + title + ",{step:" + step + ",status:" + status + "}),supplementaryView:" + err + "?null:" + jsx + "(" + supplementaryView + ",{step:" + step + ",status:" + status + "}),cta:null,isExpanded:$wfImageExpanded,onToggle:$wfToggleImageExpanded,hasSupplementaryView:!" + err + "})}"
}

func patchOriginalImagePreviewRenderers(source string) (string, bool, bool) {
	matches := imagePreviewOriginalRendererPattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source, false, false
	}
	var output strings.Builder
	last := 0
	recognized, changed := false, false
	for _, match := range matches {
		media := source[match[2]:match[3]]
		step := source[match[4]:match[5]]
		image := source[match[6]:match[7]]
		mediaURI := source[match[8]:match[9]]
		imageURI := source[match[10]:match[11]]
		resolver := source[match[12]:match[13]]
		mediaURIArgument := source[match[14]:match[15]]
		mediaPayload := source[match[16]:match[17]]
		imageInline := source[match[18]:match[19]]
		mediaInline := source[match[20]:match[21]]
		inlineData := source[match[22]:match[23]]
		mediaInlineArgument := source[match[24]:match[25]]
		if media != mediaURI || media != mediaURIArgument || media != mediaPayload || media != mediaInline || media != mediaInlineArgument || image != imageURI || image != imageInline {
			continue
		}
		recognized = true
		changed = true
		output.WriteString(source[last:match[0]])
		output.WriteString(imagePreviewV4Renderer(media, step, image, resolver, inlineData))
		last = match[1]
	}
	if !changed {
		return source, false, false
	}
	output.WriteString(source[last:])
	return output.String(), recognized, true
}

func imagePreviewV4Renderer(media, step, image, resolver, inlineData string) string {
	return "/*" + imagePreviewPatchMarker + "*/" + media + "=" + step + ".generatedMedia||" + step + ".generatedImage," + image + ";" +
		imagePreviewResolveMedia(media, image, resolver, inlineData) +
		",!" + image + "&&" + step + ".generatedImage&&" + step + ".generatedImage!==" + media + "&&(" +
		media + "=" + step + ".generatedImage," + image + "=void 0," + imagePreviewResolveMedia(media, image, resolver, inlineData) + ");"
}

// imagePreviewResolveMedia emits a comma-expression that assigns image only
// when a source is actually available. imagePreviewV4Renderer calls it first
// for generatedMedia and then, if necessary, for generatedImage; a metadata
// object in generatedMedia can no longer hide a usable generatedImage URI.
func imagePreviewResolveMedia(media, image, resolver, inlineData string) string {
	return media + "?.uri?(" + image + "=" + resolver + "?.(" + media + ".uri)," + image + "=(" + image + "&&typeof " + image + ".getState===\"function\"?" + image + ".getState():" + image + ")," + image + "=typeof " + image + "===\"string\"?" + image + ":void 0):" +
		media + "?.payload?.case===\"inlineData\"&&(" + image + "=" + media + "?" + inlineData + "(" + media + "):void 0),!" + image + "&&" + media + "?.base64Data&&(" + image + "=\"data:\"+(" + media + ".mimeType||\"image/png\")+\";base64,\"+(typeof " + media + ".base64Data===\"string\"?" + media + ".base64Data:btoa(Array.from(" + media + ".base64Data).map(i=>String.fromCharCode(i)).join(\"\"))))" +
		imagePreviewFileURIFallback(media, image)
}

func imagePreviewFileURIFallback(media, image string) string {
	// The IIFE keeps the fallback's local names out of the minified renderer
	// scope. It intentionally catches malformed percent encodings: a bad file
	// name must not throw from React rendering and blank the whole chat step.
	// VS Code maps local files to vscode-file://vscode-app/...; that is a
	// same-origin browser resource, while raw paths and file: are rejected by
	// the workbench image CSP. UNC hosts remain URI authorities.
	return ",!" + image + "&&" + media + "?.uri&&typeof " + media + ".uri===\"string\"&&" + media + ".uri.startsWith(\"file://\")&&(" + image + "=((u)=>{let p=u.replace(/^file:\\/\\//,\"\");try{p=decodeURIComponent(p)}catch{}let h=p.match(/^([^/]+)(\\/.*)$/),e=v=>encodeURI(v).replace(/[?#]/g,c=>c===\"#\"?\"%23\":\"%3F\");return h&&h[1]!==\"localhost\"?\"vscode-file://\"+h[1]+e(h[2]):\"vscode-file://vscode-app\"+e(h&&h[1]===\"localhost\"?h[2]:p)})(" + media + ".uri))"
}

func imagePreviewRendererPaths(root string) []string {
	var paths []string
	for _, relative := range imagePreviewRendererRelativePaths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	return paths
}

// imageGenerationUIRendererPaths resolves the two IDE chat bundles whose
// generated-image title/result components are owned by the workbench. Keep
// this separate from the generic preview paths because the Windows transaction
// updates their product.json checksums as one atomic plan.
func imageGenerationUIRendererPaths(root string) []string {
	paths := make([]string, 0, 2)
	for _, relative := range []string{
		"out/jetskiAgent/main.js",
		"out/vs/workbench/workbench.desktop.main.js",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	return paths
}

// imagePreviewRenderersNeedPatch reports only known, safely patchable 1.23.2
// renderer shapes that are still missing the current fallback. Missing files,
// unreadable optional bundles, and future renderer shapes intentionally do not
// count as pending: they must never make an otherwise valid endpoint patch look
// inactive.
func imagePreviewRenderersNeedPatch(paths []string) bool {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if _, result := patchImagePreviewRenderer(string(data)); result.Changed {
			return true
		}
	}
	return false
}

func imagePreviewASARRendererEntries(archive *asarArchive) []string {
	var entries []string
	for _, relative := range imagePreviewASARRelativePaths {
		node, err := archive.node(relative)
		if err != nil || node.Files != nil || node.Unpacked || node.Size == nil {
			continue
		}
		entries = append(entries, relative)
	}
	sort.Strings(entries)
	return entries
}

// imagePreviewASARUnpackedRendererPaths resolves only declared unpacked
// renderer entries. They must be patched beside app.asar, never added to the
// archive replacement map (which would silently change their ASAR semantics).
func imagePreviewASARUnpackedRendererPaths(archive *asarArchive) []string {
	var paths []string
	for _, relative := range imagePreviewASARRelativePaths {
		node, err := archive.node(relative)
		if err != nil || node.Files != nil || !node.Unpacked {
			continue
		}
		path := filepath.Join(archive.path+".unpacked", filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func imagePreviewASARUnpackedRendererPathsForPath(path string) []string {
	archive, err := readASAR(path)
	if err != nil {
		return nil
	}
	return imagePreviewASARUnpackedRendererPaths(archive)
}

func patchImagePreviewASARRenderers(archive *asarArchive, replacements map[string][]byte) bool {
	changed := false
	for _, entry := range imagePreviewASARRendererEntries(archive) {
		data, ok := replacements[entry]
		if !ok {
			var err error
			data, err = archive.readFile(entry)
			if err != nil {
				// This optional compatibility patch must never make a normal
				// endpoint patch fail merely because an unfamiliar renderer is
				// unreadable or absent.
				continue
			}
		}
		updated, result := patchImagePreviewRenderer(string(data))
		if result.Changed {
			replacements[entry] = []byte(updated)
			changed = true
		}
	}
	return changed
}

func imagePreviewASARArchiveNeedsPatch(path string) bool {
	archive, err := readASAR(path)
	if err != nil {
		return false
	}
	for _, entry := range imagePreviewASARRendererEntries(archive) {
		data, err := archive.readFile(entry)
		if err != nil {
			continue
		}
		if _, result := patchImagePreviewRenderer(string(data)); result.Changed {
			return true
		}
	}
	return false
}

func imagePreviewASARNeedsPatch(path string) bool {
	archive, err := readASAR(path)
	if err != nil {
		return false
	}
	for _, entry := range imagePreviewASARRendererEntries(archive) {
		data, err := archive.readFile(entry)
		if err != nil {
			continue
		}
		if _, result := patchImagePreviewRenderer(string(data)); result.Changed {
			return true
		}
	}
	return imagePreviewRenderersNeedPatch(imagePreviewASARUnpackedRendererPaths(archive))
}
