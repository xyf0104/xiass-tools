//go:build darwin

package patcher

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"debug/macho"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/foobaz/go-zopfli/zopfli"
)

const agentImageGenerationUIPatchMarker = "antigravity-wf:agent-image-generation-ui:v1"
const agentImageGenerationDedupePatchMarker = "antigravity-wf:agent-image-generation-dedupe:v4"
const agentImageGenerationDedupePatchV3Marker = "antigravity-wf:agent-image-generation-dedupe:v3"
const agentImageGenerationDedupePatchV2Marker = "antigravity-wf:agent-image-generation-dedupe:v2"
const agentImageGenerationDedupePatchV1Marker = "antigravity-wf:agent-image-generation-dedupe:v1"

var agentGeneratedImageComponentPattern = regexp.MustCompile(
	`var (` + imagePreviewJavaScriptIdentifier + `)=\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{var `,
)

var agentImageGenerationTitlePattern = regexp.MustCompile(
	`renderer:\(\{step:(` + imagePreviewJavaScriptIdentifier + `),status:(` + imagePreviewJavaScriptIdentifier + `),error:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{var ` +
		`(` + imagePreviewJavaScriptIdentifier + `)=!!(` + imagePreviewJavaScriptIdentifier + `)\.generatedMedia\?\.uri,` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\.modelName\?(` + imagePreviewJavaScriptIdentifier + `)\[(` + imagePreviewJavaScriptIdentifier + `)\.modelName\]:void 0,` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.displayName\|\|"Gemini";` +
		`(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\?\.isNewModel\?\?!1;` +
		`(` + imagePreviewJavaScriptIdentifier + `)=`,
)

var agentImageArtifactMarkdownRendererPrefixPattern = regexp.MustCompile(
	`var (` + imagePreviewJavaScriptIdentifier + `)=\(\{src:(` + imagePreviewJavaScriptIdentifier + `),alt:(` + imagePreviewJavaScriptIdentifier + `),originalFilePath:(` + imagePreviewJavaScriptIdentifier + `),popout:(` + imagePreviewJavaScriptIdentifier + `)=!0,className:(` + imagePreviewJavaScriptIdentifier + `)="",openUri:(` + imagePreviewJavaScriptIdentifier + `)\}\)=>\{var \[(` + imagePreviewJavaScriptIdentifier + `),(` + imagePreviewJavaScriptIdentifier + `)\]=\(0,(` + imagePreviewJavaScriptIdentifier + `)\.useState\)\(!1\),(` + imagePreviewJavaScriptIdentifier + `)=(` + imagePreviewJavaScriptIdentifier + `)\((` + imagePreviewJavaScriptIdentifier + `)\),`,
)

var agentMinifiedNextDeclarationPattern = regexp.MustCompile(`};\s*(?:var|const|function)\s+`)

type agentImageUIPatchResult struct {
	ArchiveRecognized bool
	UIRecognized      bool
	Changed           bool
}

func patchAgentImageUI(source string) (string, imagePreviewPatchResult) {
	result := imagePreviewPatchResult{
		Recognized: strings.Contains(source, agentImageGenerationUIPatchMarker) &&
			strings.Contains(source, agentImageGenerationDedupePatchMarker),
	}
	if result.Recognized {
		return source, result
	}

	updated, titleRecognized, titleChanged := patchAgentImageGenerationTitle(source)
	if !titleRecognized {
		return source, result
	}

	updated, dedupeRecognized, dedupeChanged := patchAgentDuplicateGeneratedImage(updated)
	if !dedupeRecognized {
		return source, result
	}
	result.Recognized = true
	result.Changed = titleChanged || dedupeChanged
	return updated, result
}

func patchAgentImageGenerationTitle(source string) (string, bool, bool) {
	if strings.Contains(source, agentImageGenerationUIPatchMarker) {
		return source, true, false
	}
	matches := agentImageGenerationTitlePattern.FindAllStringSubmatchIndex(source, -1)
	if len(matches) != 1 {
		return source, false, false
	}
	match := matches[0]
	step := imagePreviewSubmatch(source, match, 1)
	status := imagePreviewSubmatch(source, match, 2)
	hasMedia := imagePreviewSubmatch(source, match, 4)
	resolvedModel := imagePreviewSubmatch(source, match, 6)
	modelMap := imagePreviewSubmatch(source, match, 8)
	displayName := imagePreviewSubmatch(source, match, 10)
	isNewModel := imagePreviewSubmatch(source, match, 12)
	title := imagePreviewSubmatch(source, match, 14)
	if !sameImagePreviewIdentifiers(step,
		imagePreviewSubmatch(source, match, 5),
		imagePreviewSubmatch(source, match, 7),
		imagePreviewSubmatch(source, match, 9),
	) || !sameImagePreviewIdentifiers(resolvedModel,
		imagePreviewSubmatch(source, match, 11),
		imagePreviewSubmatch(source, match, 13),
	) || modelMap == "" || title == "" {
		return source, false, false
	}
	endOffset := strings.Index(source[match[1]:], ";return ")
	if endOffset < 0 || endOffset > 2*1024 {
		return source, false, false
	}
	end := match[1] + endOffset
	current := source[match[0]:end]
	oldModelLabel := displayName + `=` + resolvedModel + `?.displayName||"Gemini"`
	if strings.Count(current, oldModelLabel) != 1 {
		return source, false, false
	}
	updated := strings.Replace(current, oldModelLabel, imageGenerationModelLabel(step, resolvedModel, displayName), 1)
	oldModelFlag := isNewModel + `=` + resolvedModel + `?.isNewModel??!1;`
	newModelFlag := oldModelFlag + `var $wfIsGeminiImage=!!` + resolvedModel + `||/^gemini[-_]/i.test(` + step + `.modelName||"");`
	if strings.Count(updated, oldModelFlag) != 1 {
		return source, false, false
	}
	updated = strings.Replace(updated, oldModelFlag, newModelFlag, 1)
	generatedTitleBranchPattern := regexp.MustCompile(`:\s*` + regexp.QuoteMeta(hasMedia) + `\?` + "`Generated with ")
	if len(generatedTitleBranchPattern.FindAllStringIndex(updated, -1)) != 1 {
		return source, false, false
	}
	for _, prefix := range []string{"Generating with ", "Generated with ", "Generate with "} {
		oldTitle := "`" + prefix + "${" + displayName + `} \ud83c\udf4c`
		newTitle := "`" + prefix + "${" + displayName + `}${$wfIsGeminiImage?" \ud83c\udf4c":""}`
		if strings.Count(updated, oldTitle) != 1 {
			return source, false, false
		}
		updated = strings.Replace(updated, oldTitle, newTitle, 1)
	}
	loadingExpression := ""
	loadingPattern := regexp.MustCompile(regexp.QuoteMeta(title+`=`) + `(` + imagePreviewJavaScriptIdentifier + `\(` + regexp.QuoteMeta(status) + `\))\?`)
	loadingMatches := loadingPattern.FindAllStringSubmatch(updated, -1)
	if len(loadingMatches) == 1 {
		loadingExpression = loadingMatches[0][1]
	} else {
		// Agent 2.0.10 inlines the same verified loading-status set instead of
		// calling the helper used by 2.3.1 and 2.6.0.
		inlineLoadingPattern := regexp.MustCompile(regexp.QuoteMeta(title+`=`) + `(\[8,9,1,2,11\]\.includes\(` + regexp.QuoteMeta(status) + `\))\?`)
		inlineLoadingMatches := inlineLoadingPattern.FindAllStringSubmatch(updated, -1)
		if len(loadingMatches) != 0 || len(inlineLoadingMatches) != 1 {
			return source, false, false
		}
		loadingExpression = inlineLoadingMatches[0][1]
	}
	originalLoading := title + `=` + loadingExpression + `?`
	updated = strings.Replace(updated, originalLoading,
		title+`=`+loadingExpression+`&&!`+step+`.modelName?`+"`Generating image`"+`:`+loadingExpression+`?`, 1)
	updated = "/*" + agentImageGenerationUIPatchMarker + "*//*" + imageGenerationUIPatchMarker + "*/" + updated
	return source[:match[0]] + updated + source[end:], true, true
}

func patchAgentDuplicateGeneratedImage(source string) (string, bool, bool) {
	if strings.Contains(source, agentImageGenerationDedupePatchMarker) {
		return source, true, false
	}
	if strings.Contains(source, agentImageGenerationDedupePatchV3Marker) {
		updated := strings.ReplaceAll(source, agentImageGenerationDedupePatchV3Marker, agentImageGenerationDedupePatchMarker)
		updated, ok := addAgentImageArtifactThumbnail(updated)
		if !ok {
			return source, true, false
		}
		return updated, true, updated != source
	}
	if strings.Contains(source, agentImageGenerationDedupePatchV2Marker) {
		legacy := agentGeneratedImageDedupeV2Registry()
		if !strings.Contains(source, legacy) {
			return source, true, false
		}
		updated := strings.ReplaceAll(source, legacy, agentGeneratedImageDedupeRegistry())
		updated = strings.ReplaceAll(updated, agentImageGenerationDedupePatchV2Marker, agentImageGenerationDedupePatchMarker)
		updated, ok := addAgentImageArtifactThumbnail(updated)
		if !ok {
			return source, true, false
		}
		return updated, true, updated != source
	}
	if strings.Contains(source, agentImageGenerationDedupePatchV1Marker) {
		legacy := agentGeneratedImageDedupeV1Registry()
		if !strings.Contains(source, legacy) {
			return source, true, false
		}
		updated := strings.ReplaceAll(source, legacy, agentGeneratedImageDedupeRegistry())
		updated = strings.ReplaceAll(updated, agentImageGenerationDedupePatchV1Marker, agentImageGenerationDedupePatchMarker)
		updated, ok := addAgentImageArtifactThumbnail(updated)
		if !ok {
			return source, true, false
		}
		return updated, true, updated != source
	}

	previewNeedle := `alt:"Generated image preview"`
	previewOffsets := allStringOffsets(source, previewNeedle)
	markdownCandidates := agentImageArtifactMarkdownRendererPrefixPattern.FindAllStringSubmatchIndex(source, -1)
	var markdownMatches [][]int
	for _, candidate := range markdownCandidates {
		end := agentMinifiedDeclarationEnd(source, candidate[1], 8*1024)
		if end < 0 {
			continue
		}
		alt := imagePreviewSubmatch(source, candidate, 3)
		if strings.Contains(source[candidate[1]:end], `alt:`+alt+`||"Artifact image"`) {
			markdownMatches = append(markdownMatches, candidate)
		}
	}
	if len(previewOffsets) != 1 || len(markdownMatches) != 1 {
		return source, false, false
	}

	previewOffset := previewOffsets[0]
	previewSearchStart := previewOffset - 6*1024
	if previewSearchStart < 0 {
		previewSearchStart = 0
	}
	prefixMatches := agentGeneratedImageComponentPattern.FindAllStringSubmatchIndex(source[previewSearchStart:previewOffset], -1)
	if len(prefixMatches) == 0 {
		return source, false, false
	}
	prefixMatch := prefixMatches[len(prefixMatches)-1]
	for index := range prefixMatch {
		if prefixMatch[index] >= 0 {
			prefixMatch[index] += previewSearchStart
		}
	}
	step := imagePreviewSubmatch(source, prefixMatch, 2)
	componentStart := prefixMatch[0]
	componentEndOffset := strings.Index(source[previewOffset:], "):null};")
	if componentEndOffset < 0 || componentEndOffset > 4*1024 {
		return source, false, false
	}
	componentEnd := previewOffset + componentEndOffset + len("):null};")
	component := source[componentStart:componentEnd]
	mediaPattern := regexp.MustCompile(`,(` + imagePreviewJavaScriptIdentifier + `)=` + regexp.QuoteMeta(step) + `\.generatedMedia,`)
	mediaMatch := mediaPattern.FindStringSubmatch(component)
	resolvedPattern := regexp.MustCompile(`,(` + imagePreviewJavaScriptIdentifier + `)=void 0;`)
	resolvedMatch := resolvedPattern.FindStringSubmatch(component)
	if mediaMatch == nil || resolvedMatch == nil || strings.Count(component, previewNeedle) != 1 {
		return source, false, false
	}
	media, resolved := mediaMatch[1], resolvedMatch[1]
	returnOffset := strings.LastIndex(component[:strings.Index(component, previewNeedle)], ";return ")
	if returnOffset < 0 || strings.Count(component[:strings.Index(component, previewNeedle)], ";return ") != 1 {
		return source, false, false
	}
	registrationOffset := componentStart + returnOffset + 1
	registration := `$wfRememberGeneratedImageURI(` + media + `?.uri,` + resolved + `);`

	markdownMatch := markdownMatches[0]
	sourceValue := imagePreviewSubmatch(source, markdownMatch, 2)
	originalPath := imagePreviewSubmatch(source, markdownMatch, 4)
	errorState := imagePreviewSubmatch(source, markdownMatch, 8)
	resolvedValue := imagePreviewSubmatch(source, markdownMatch, 11)
	if sourceValue != imagePreviewSubmatch(source, markdownMatch, 13) {
		return source, false, false
	}
	markdownEnd := agentMinifiedDeclarationEnd(source, markdownMatch[1], 8*1024)
	if markdownEnd < 0 {
		return source, false, false
	}
	markdownSegment := source[markdownMatch[1]:markdownEnd]
	thumbnailNeedle := `alt:` + imagePreviewSubmatch(source, markdownMatch, 3) + `||"Artifact image",`
	thumbnailCount := strings.Count(source[markdownMatch[0]:markdownEnd], thumbnailNeedle)
	if thumbnailCount != 1 {
		return source, false, false
	}
	thumbnailOffset := markdownMatch[0] + strings.Index(source[markdownMatch[0]:markdownEnd], thumbnailNeedle) + len(thumbnailNeedle)
	duplicateExpression := `$wfIsDuplicateGeneratedImageURI(` + sourceValue + `,` + resolvedValue + `,` + originalPath + `)`
	var duplicateReplacement imagePreviewRendererReplacement
	if returnNeedle := `;if(!` + sourceValue + `||` + errorState + `)return `; strings.Count(markdownSegment, returnNeedle) == 1 {
		returnIndex := strings.Index(markdownSegment, returnNeedle)
		duplicateReplacement = imagePreviewRendererReplacement{
			start: markdownMatch[1] + returnIndex + 1,
			end:   markdownMatch[1] + returnIndex + 1,
			value: `if(` + duplicateExpression + `)return null;`,
		}
	} else if returnNeedle := `;return!` + sourceValue + `||` + errorState + `?`; strings.Count(markdownSegment, returnNeedle) == 1 {
		returnIndex := strings.Index(markdownSegment, returnNeedle)
		duplicateReplacement = imagePreviewRendererReplacement{
			start: markdownMatch[1] + returnIndex,
			end:   markdownMatch[1] + returnIndex + len(returnNeedle),
			value: `;return ` + duplicateExpression + `?null:!` + sourceValue + `||` + errorState + `?`,
		}
	} else {
		return source, false, false
	}

	registry := `/*` + agentImageGenerationDedupePatchMarker + `*/` + agentGeneratedImageDedupeRegistry()

	replacements := []imagePreviewRendererReplacement{
		{start: registrationOffset, end: registrationOffset, value: registration},
		{start: markdownMatch[0], end: markdownMatch[0], value: registry},
		{start: thumbnailOffset, end: thumbnailOffset, value: imageArtifactThumbnailStyle},
		duplicateReplacement,
	}
	// Apply from the end so the offsets remain valid.
	for left := 0; left < len(replacements); left++ {
		for right := left + 1; right < len(replacements); right++ {
			if replacements[left].start < replacements[right].start {
				replacements[left], replacements[right] = replacements[right], replacements[left]
			}
		}
	}
	updated := source
	for _, replacement := range replacements {
		updated = updated[:replacement.start] + replacement.value + updated[replacement.end:]
	}
	return updated, true, true
}

func addAgentImageArtifactThumbnail(source string) (string, bool) {
	marker := "/*" + agentImageGenerationDedupePatchMarker + "*/"
	if strings.Count(source, marker) != 1 {
		return source, false
	}
	var matches [][]int
	for _, candidate := range agentImageArtifactMarkdownRendererPrefixPattern.FindAllStringSubmatchIndex(source, -1) {
		end := agentMinifiedDeclarationEnd(source, candidate[1], 8*1024)
		if end < 0 {
			continue
		}
		alt := imagePreviewSubmatch(source, candidate, 3)
		segment := source[candidate[0]:end]
		if strings.Contains(segment, `alt:`+alt+`||"Artifact image"`) && strings.Contains(segment, `$wfIsDuplicateGeneratedImageURI`) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return source, false
	}
	match := matches[0]
	end := agentMinifiedDeclarationEnd(source, match[1], 8*1024)
	component := source[match[0]:end]
	if strings.Contains(component, imageArtifactThumbnailStyle) {
		return source, true
	}
	needle := `alt:` + imagePreviewSubmatch(source, match, 3) + `||"Artifact image",`
	if strings.Count(component, needle) != 1 {
		return source, false
	}
	insert := match[0] + strings.Index(component, needle) + len(needle)
	return source[:insert] + imageArtifactThumbnailStyle + source[insert:], true
}

func agentGeneratedImageDedupeV1Registry() string {
	return `const $wfGeneratedImageURIs=new Map,$wfGeneratedImageURIKey=value=>{if(typeof value!=="string"||!value||value.startsWith("data:"))return"";let text=value;try{text=decodeURIComponent(text)}catch{}return text.replace(/^vscode-file:\/\/(?:vscode-app)?/i,"").replace(/^file:\/\//i,"").replace(/\\/g,"/").replace(/[?#].*$/,"").toLowerCase()},$wfPruneGeneratedImageURIs=()=>{let now=Date.now();for(let[key,time]of $wfGeneratedImageURIs)now-time>6E5&&$wfGeneratedImageURIs.delete(key);return now},$wfRememberGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs();for(let value of values){let key=$wfGeneratedImageURIKey(value);key&&$wfGeneratedImageURIs.set(key,now)}},$wfIsDuplicateGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs();for(let value of values){let key=$wfGeneratedImageURIKey(value),time=$wfGeneratedImageURIs.get(key);if(time!==void 0&&now-time<=6E5)return!0}return!1};`
}

func agentGeneratedImageDedupeRegistry() string {
	return `const $wfGeneratedImageURIs=new Map,$wfGeneratedImageEvents=[],$wfGeneratedImageURIKey=value=>{if(typeof value!=="string"||!value||value.startsWith("data:"))return"";let text=value;try{text=decodeURIComponent(text)}catch{}return text.replace(/^vscode-file:\/\/(?:vscode-app)?/i,"").replace(/^file:\/\//i,"").replace(/\\/g,"/").replace(/[?#].*$/,"").toLowerCase()},$wfPruneGeneratedImageURIs=()=>{let now=Date.now();for(let[key,time]of $wfGeneratedImageURIs)now-time>6E5&&$wfGeneratedImageURIs.delete(key);for(let index=$wfGeneratedImageEvents.length-1;index>=0;index--)now-$wfGeneratedImageEvents[index].time>6E5&&$wfGeneratedImageEvents.splice(index,1);return now},$wfRememberGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs(),keys=values.map($wfGeneratedImageURIKey).filter(Boolean),recent=$wfGeneratedImageEvents.some(event=>now-event.time<6E5&&event.keys.some(key=>keys.includes(key)));for(let key of keys)$wfGeneratedImageURIs.set(key,now);keys.length&&!recent&&$wfGeneratedImageEvents.push({keys,time:now,consumed:!1})},$wfIsDuplicateGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs(),keys=values.map($wfGeneratedImageURIKey).filter(Boolean),event=$wfGeneratedImageEvents.find(candidate=>!candidate.consumed&&candidate.keys.some(key=>keys.includes(key))),exact=keys.some(key=>{let time=$wfGeneratedImageURIs.get(key);return time!==void 0&&now-time<=6E5});event||(event=$wfGeneratedImageEvents.find(candidate=>!candidate.consumed));if(event)event.consumed=!0;return exact||!!event};`
}

func agentGeneratedImageDedupeV2Registry() string {
	return `const $wfGeneratedImageURIs=new Map,$wfGeneratedImageEvents=[],$wfGeneratedImageURIKey=value=>{if(typeof value!=="string"||!value||value.startsWith("data:"))return"";let text=value;try{text=decodeURIComponent(text)}catch{}return text.replace(/^vscode-file:\/\/(?:vscode-app)?/i,"").replace(/^file:\/\//i,"").replace(/\\/g,"/").replace(/[?#].*$/,"").toLowerCase()},$wfPruneGeneratedImageURIs=()=>{let now=Date.now();for(let[key,time]of $wfGeneratedImageURIs)now-time>6E5&&$wfGeneratedImageURIs.delete(key);for(let index=$wfGeneratedImageEvents.length-1;index>=0;index--)now-$wfGeneratedImageEvents[index].time>12E4&&$wfGeneratedImageEvents.splice(index,1);return now},$wfRememberGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs(),keys=values.map($wfGeneratedImageURIKey).filter(Boolean),recent=$wfGeneratedImageEvents.some(event=>!event.consumed&&now-event.time<5E3&&event.keys.some(key=>keys.includes(key)));for(let key of keys)$wfGeneratedImageURIs.set(key,now);keys.length&&!recent&&$wfGeneratedImageEvents.push({keys,time:now,consumed:!1})},$wfIsDuplicateGeneratedImageURI=(...values)=>{let now=$wfPruneGeneratedImageURIs(),keys=values.map($wfGeneratedImageURIKey).filter(Boolean),event=$wfGeneratedImageEvents.find(candidate=>!candidate.consumed&&candidate.keys.some(key=>keys.includes(key))),exact=keys.some(key=>{let time=$wfGeneratedImageURIs.get(key);return time!==void 0&&now-time<=6E5});event||(event=$wfGeneratedImageEvents.find(candidate=>!candidate.consumed));if(event)event.consumed=!0;return exact||!!event};`
}

func agentMinifiedDeclarationEnd(source string, from, limit int) int {
	end := from + limit
	if end > len(source) {
		end = len(source)
	}
	segment := source[from:end]
	match := agentMinifiedNextDeclarationPattern.FindStringIndex(segment)
	if match == nil {
		return -1
	}
	return from + match[0] + 2
}

func allStringOffsets(source, needle string) []int {
	var offsets []int
	for from := 0; from <= len(source)-len(needle); {
		offset := strings.Index(source[from:], needle)
		if offset < 0 {
			break
		}
		offset += from
		offsets = append(offsets, offset)
		from = offset + len(needle)
	}
	return offsets
}

func patchAgentEmbeddedUIArchive(data []byte) ([]byte, agentImageUIPatchResult, error) {
	archive, err := readAgentEmbeddedUIArchive(data)
	if err != nil {
		return data, agentImageUIPatchResult{}, nil
	}
	result := agentImageUIPatchResult{ArchiveRecognized: true}
	mainData, err := readAgentEmbeddedUIEntry(archive, "main.js")
	if err != nil {
		return nil, result, err
	}
	updatedMain, patchResult := patchAgentImageUI(string(mainData))
	result.UIRecognized = patchResult.Recognized
	result.Changed = patchResult.Changed
	if !patchResult.Recognized {
		return nil, result, fmt.Errorf("Antigravity 2.0 内嵌图片界面结构尚未通过安全匹配；未修改任何文件")
	}
	if !patchResult.Changed {
		return data, result, nil
	}

	originalLength := archive.end - archive.start
	// Previous WF revisions filled the fixed PE slot with trailing spaces in
	// ZIP comments. Reclaim only that application-owned padding during a v1
	// migration; otherwise the old padding and the larger v2 renderer are both
	// counted and a valid forward upgrade appears to exceed the slot.
	legacyPadding := bytes.Contains(mainData, []byte(agentImageGenerationDedupePatchV1Marker)) ||
		bytes.Contains(mainData, []byte(agentImageGenerationDedupePatchV3Marker)) ||
		bytes.Contains(mainData, []byte(agentImageGenerationDedupePatchV2Marker))
	baseArchiveComment := archive.reader.Comment
	if legacyPadding {
		baseArchiveComment = strings.TrimRight(baseArchiveComment, " ")
	}
	build := func(comment, mainEntryCommentPadding string) ([]byte, error) {
		var output bytes.Buffer
		writer := zip.NewWriter(&output)
		writer.RegisterCompressor(zip.Deflate, func(destination io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(destination, flate.BestCompression)
		})
		if err := writer.SetComment(comment); err != nil {
			return nil, err
		}
		recompress := map[string]bool{"main.js": true}
		for _, stylesheet := range archive.stylesheets {
			recompress[stylesheet] = true
		}
		for _, entry := range archive.reader.File {
			if !recompress[entry.Name] {
				if err := writer.Copy(entry); err != nil {
					return nil, err
				}
				continue
			}
			entryData := []byte(updatedMain)
			if entry.Name != "main.js" {
				var readErr error
				entryData, readErr = readAgentEmbeddedUIEntry(archive, entry.Name)
				if readErr != nil {
					return nil, readErr
				}
			}
			header := entry.FileHeader
			if legacyPadding && entry.Name == "main.js" {
				header.Comment = strings.TrimRight(header.Comment, " ")
			}
			header.CRC32 = 0
			header.CompressedSize = 0
			header.CompressedSize64 = 0
			header.UncompressedSize = 0
			header.UncompressedSize64 = 0
			if entry.Name == "main.js" {
				header.Comment += mainEntryCommentPadding
				var compressed bytes.Buffer
				options := zopfli.DefaultOptions()
				options.NumIterations = 3
				if compressErr := zopfli.DeflateCompress(&options, entryData, &compressed); compressErr != nil {
					return nil, compressErr
				}
				header.Flags &^= 0x8
				header.CRC32 = crc32.ChecksumIEEE(entryData)
				header.CompressedSize64 = uint64(compressed.Len())
				header.UncompressedSize64 = uint64(len(entryData))
				entryWriter, createErr := writer.CreateRaw(&header)
				if createErr != nil {
					return nil, createErr
				}
				if _, writeErr := entryWriter.Write(compressed.Bytes()); writeErr != nil {
					return nil, writeErr
				}
				continue
			}
			entryWriter, createErr := writer.CreateHeader(&header)
			if createErr != nil {
				return nil, createErr
			}
			if _, writeErr := entryWriter.Write(entryData); writeErr != nil {
				return nil, writeErr
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return output.Bytes(), nil
	}

	rebuilt, err := build(baseArchiveComment, "")
	if err != nil {
		return nil, result, fmt.Errorf("重建 Antigravity 2.0 内嵌 UI 失败: %w", err)
	}
	padding := originalLength - len(rebuilt)
	if padding < 0 {
		return nil, result, fmt.Errorf("Antigravity 2.0 内嵌 UI 补丁超出原始保留空间 %d 字节；未修改任何文件", -padding)
	}
	if padding > 0 {
		archivePadding := padding
		if available := 65535 - len(baseArchiveComment); archivePadding > available {
			archivePadding = available
		}
		entryPadding := padding - archivePadding
		mainEntryCommentLength := 0
		for _, entry := range archive.reader.File {
			if entry.Name == "main.js" {
				mainEntryCommentLength = len(entry.Comment)
				if legacyPadding {
					mainEntryCommentLength = len(strings.TrimRight(entry.Comment, " "))
				}
				break
			}
		}
		if entryPadding > 65535-mainEntryCommentLength {
			return nil, result, fmt.Errorf("Antigravity 2.0 内嵌 UI 等长填充需要 %d 字节，超过 ZIP 安全上限；未修改任何文件", padding)
		}
		rebuilt, err = build(
			baseArchiveComment+strings.Repeat(" ", archivePadding),
			strings.Repeat(" ", entryPadding),
		)
		if err != nil {
			return nil, result, fmt.Errorf("等长重建 Antigravity 2.0 内嵌 UI 失败: %w", err)
		}
	}
	if len(rebuilt) != originalLength {
		return nil, result, fmt.Errorf("Antigravity 2.0 内嵌 UI 重建长度不一致: %d != %d", len(rebuilt), originalLength)
	}
	updated := append([]byte(nil), data...)
	copy(updated[archive.start:archive.end], rebuilt)
	verified, verifyErr := readAgentEmbeddedUIArchive(updated)
	if verifyErr != nil {
		return nil, result, fmt.Errorf("重建后的 Antigravity 2.0 内嵌 UI 无法读取: %w", verifyErr)
	}
	verifiedMain, verifyErr := readAgentEmbeddedUIEntry(verified, "main.js")
	if verifyErr != nil || !bytes.Contains(verifiedMain, []byte(agentImageGenerationUIPatchMarker)) ||
		!bytes.Contains(verifiedMain, []byte(agentImageGenerationDedupePatchMarker)) {
		return nil, result, fmt.Errorf("重建后的 Antigravity 2.0 图片界面标记校验失败")
	}
	return updated, result, nil
}

func darwinAgentEmbeddedUIPatchState(path string) (hasArchive, patched bool) {
	if path == "" {
		return false, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	return darwinAgentEmbeddedUIPatchStateData(data)
}

func darwinAgentEmbeddedUIPatchStateData(data []byte) (hasArchive, patched bool) {
	archive, err := readAgentEmbeddedUIArchive(data)
	if err != nil {
		return false, false
	}
	mainData, err := readAgentEmbeddedUIEntry(archive, "main.js")
	if err != nil {
		return true, false
	}
	return true, bytes.Contains(mainData, []byte(agentImageGenerationUIPatchMarker)) &&
		bytes.Contains(mainData, []byte(agentImageGenerationDedupePatchMarker))
}

func prepareDarwinAgentEmbeddedUIPlan(plan *patchPlan) (*patchPlan, bool, error) {
	if plan == nil {
		return nil, false, nil
	}
	updated, result, err := patchAgentEmbeddedUIArchive(plan.updated)
	if err != nil {
		return nil, result.ArchiveRecognized, err
	}
	if !result.ArchiveRecognized {
		return plan, false, nil
	}
	plan.updated = updated
	plan.changed = plan.changed || result.Changed
	return plan, true, nil
}

func validateDarwinAgentEmbeddedUISource(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	binary, err := macho.Open(path)
	if err != nil {
		return false, fmt.Errorf("Antigravity 2.0 Language Server 不是有效 Mach-O: %w", err)
	}
	if closeErr := binary.Close(); closeErr != nil {
		return false, closeErr
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	archive, err := readAgentEmbeddedUIArchive(data)
	if err != nil {
		return false, nil
	}
	mainData, err := readAgentEmbeddedUIEntry(archive, "main.js")
	if err != nil {
		return true, err
	}
	_, result := patchAgentImageUI(string(mainData))
	if !result.Recognized {
		return true, fmt.Errorf("Antigravity 2.0 内嵌图片界面结构尚未通过安全匹配")
	}
	return true, nil
}
