package documentcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	jsonvalue "github.com/leafrun/leafrun-contracts/jsonvalue/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	projectInputFileCountLimit = 64
	projectInputFileBytesLimit = 25 << 20
)

// FileRef identifies a caller-authorized Storage resource by logical field
// pointer. Hash and size are frozen metadata supplied by the trusted resolver.
type FileRef struct {
	Pointer    string `json:"pointer"`
	ResourceID string `json:"resourceId"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// PreparedFile binds one schema field to its frozen resource and deterministic
// path inside an isolated execution snapshot.
type PreparedFile struct {
	Pointer    string `json:"pointer"`
	ResourceID string `json:"resourceId"`
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	Path       string `json:"path"`
}

// InputFileOpener supplies bytes for a previously authorized, frozen resource.
// Core verifies the exact length and SHA-256 before adding bytes to a snapshot.
type InputFileOpener func(context.Context, PreparedFile) (io.ReadCloser, error)

var safeInputExtension = regexp.MustCompile(`^\.[A-Za-z0-9]{1,16}$`)
var lowerSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// CreatePreparedSnapshot copies prepared JSON and verified file bytes into a
// new snapshot. The source and its captured font trees remain unchanged.
func CreatePreparedSnapshot(ctx context.Context, base *Snapshot, prepared PreparedInput, open InputFileOpener, options CreateInputSnapshotOptions) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", err)
	}
	if base == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("base snapshot is required"))
	}
	parameters, err := decodeCanonicalPreparedJSON(prepared.JSON)
	if err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "input.json", err)
	}
	if err := validatePreparedInputStructure("input.json", prepared, parameters); err != nil {
		return nil, snapshotFailure(SnapshotStageValidate, "input.json", err)
	}
	manifest := base.Manifest()
	for _, directory := range manifest.Directories {
		if directory == ".leafrun-input" || strings.HasPrefix(directory, ".leafrun-input/") {
			return nil, snapshotFailure(SnapshotStageValidate, directory, errors.New("source snapshot owns the reserved .leafrun-input root"))
		}
	}
	for _, file := range manifest.Files {
		if file.Path == ".leafrun-input" || strings.HasPrefix(file.Path, ".leafrun-input/") {
			return nil, snapshotFailure(SnapshotStageValidate, file.Path, errors.New("source snapshot owns the reserved .leafrun-input root"))
		}
	}
	if len(prepared.Files) > 0 && open == nil {
		return nil, snapshotFailure(SnapshotStageValidate, "", errors.New("input file opener is required"))
	}
	contents := make(map[string][]byte, len(prepared.Files)+1)
	contents["input.json"] = bytes.Clone(prepared.JSON)
	seenContent := make(map[string][]byte, len(prepared.Files))
	for _, file := range prepared.Files {
		if err := ctx.Err(); err != nil {
			return nil, snapshotFailure(SnapshotStageAcquire, file.Path, err)
		}
		// The generated path includes the safe extension. Same bytes with two
		// extensions are two snapshot files and count twice toward the budget.
		contentKey := file.Path
		data, seen := seenContent[contentKey]
		if !seen {
			reader, openErr := open(ctx, file)
			if openErr != nil {
				return nil, snapshotFailure(SnapshotStageAcquire, file.Path, openErr)
			}
			if reader == nil {
				return nil, snapshotFailure(SnapshotStageAcquire, file.Path, errors.New("input file opener returned a nil reader"))
			}
			data, err = io.ReadAll(io.LimitReader(reader, file.SizeBytes+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return nil, snapshotFailure(SnapshotStageCapture, file.Path, errors.Join(err, closeErr))
			}
			if int64(len(data)) != file.SizeBytes {
				return nil, snapshotFailure(SnapshotStageVerify, file.Path, errors.New("input resource size changed after authorization"))
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != file.SHA256 {
				return nil, snapshotFailure(SnapshotStageVerify, file.Path, errors.New("input resource SHA-256 changed after authorization"))
			}
			seenContent[contentKey] = bytes.Clone(data)
		}
		if existing, exists := contents[file.Path]; exists && !bytes.Equal(existing, data) {
			return nil, snapshotFailure(SnapshotStageValidate, file.Path, errors.New("generated input path collision"))
		}
		contents[file.Path] = bytes.Clone(data)
	}
	if err := ctx.Err(); err != nil {
		return nil, snapshotFailure(SnapshotStageAcquire, "", err)
	}
	return createInputOverlaySnapshot(ctx, base, contents, options)
}

func validatePreparedInputStructure(file string, prepared PreparedInput, parameters map[string]any) error {
	if int64(len(prepared.JSON)) > projectPreparedJSONLimit {
		return projectError(file, "", "prepared_parameters_too_large", "prepared parameters exceed 1 MiB")
	}
	normalized, err := jsonvalue.NormalizeObject(prepared.JSON)
	if err != nil {
		return projectParametersJSONError(file, err)
	}
	if !bytes.Equal(normalized, prepared.JSON) {
		return projectError(file, "", "prepared_json_not_canonical", "prepared JSON must use canonical Leafrun JSON encoding")
	}
	if prepared.Files == nil {
		return projectError(file, "", "prepared_input_invalid", "prepared files must be an explicit array")
	}
	if len(prepared.Files) > projectInputFileCountLimit {
		return projectError(file, "", "file_reference_limit", "at most 64 file references are allowed")
	}
	if err := validateTypstInputs(prepared.Inputs, true); err != nil {
		return projectError(file, "", "prepared_input_invalid", err.Error())
	}
	seenPointers := make(map[string]bool, len(prepared.Files))
	seenPaths := make(map[string]PreparedFile, len(prepared.Files))
	contentSizes := make(map[string]int64, len(prepared.Files))
	digestSizes := make(map[string]int64, len(prepared.Files))
	previousPointer := ""
	for _, preparedFile := range prepared.Files {
		ref := FileRef{Pointer: preparedFile.Pointer, ResourceID: preparedFile.ResourceID, Filename: preparedFile.Filename, SHA256: preparedFile.SHA256, SizeBytes: preparedFile.SizeBytes}
		if err := validateFileRef(file, ref); err != nil {
			return err
		}
		if seenPointers[preparedFile.Pointer] {
			return projectError(file, preparedFile.Pointer, "duplicate_file_reference", "a parameter pointer can have only one file reference")
		}
		if previousPointer != "" && preparedFile.Pointer < previousPointer {
			return projectError(file, preparedFile.Pointer, "prepared_files_not_canonical", "prepared file descriptors must be sorted by JSON Pointer")
		}
		previousPointer = preparedFile.Pointer
		seenPointers[preparedFile.Pointer] = true
		wantPath := generatedInputPath(preparedFile.SHA256, preparedFile.Filename)
		if preparedFile.Path != wantPath {
			return projectError(file, preparedFile.Pointer, "prepared_file_path_invalid", "generated file path does not match its frozen descriptor")
		}
		if old, exists := seenPaths[preparedFile.Path]; exists && (old.SHA256 != preparedFile.SHA256 || old.SizeBytes != preparedFile.SizeBytes) {
			return projectError(file, preparedFile.Pointer, "prepared_file_path_collision", "generated file path has conflicting descriptors")
		}
		if old, exists := digestSizes[preparedFile.SHA256]; exists && old != preparedFile.SizeBytes {
			return projectError(file, preparedFile.Pointer, "prepared_file_path_collision", "one SHA-256 cannot identify different byte lengths")
		}
		seenPaths[preparedFile.Path] = preparedFile
		digestSizes[preparedFile.SHA256] = preparedFile.SizeBytes
		contentSizes[preparedFile.Path] = preparedFile.SizeBytes
	}
	var total int64
	for _, size := range contentSizes {
		if size > projectInputFileBytesLimit-total {
			return projectError(file, "", "input_file_bytes_limit", "unique input file bytes exceed 25 MiB")
		}
		total += size
	}
	for _, preparedFile := range prepared.Files {
		value, exists := lookupProjectPointer(parameters, preparedFile.Pointer)
		if !exists || value != "/"+preparedFile.Path {
			return projectError(file, preparedFile.Pointer, "prepared_file_binding_mismatch", "prepared JSON does not contain the generated path for its file descriptor")
		}
	}
	return nil
}

func decodeCanonicalPreparedJSON(data []byte) (map[string]any, error) {
	if int64(len(data)) > projectPreparedJSONLimit {
		return nil, errors.New("prepared parameters exceed 1 MiB")
	}
	value, err := jsonvalue.Decode(data, projectPreparedJSONLimit)
	if err != nil {
		return nil, err
	}
	parameters, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("prepared parameters must be a JSON object")
	}
	return parameters, nil
}

func validateFileRef(file string, ref FileRef) error {
	if _, err := decodeProjectJSONPointer(ref.Pointer); err != nil || ref.Pointer == "" {
		return projectError(file, ref.Pointer, "invalid_file_reference", "file pointer must be a non-root RFC 6901 JSON Pointer")
	}
	if ref.ResourceID == "" || len(ref.ResourceID) > 256 || !utf8.ValidString(ref.ResourceID) || strings.ContainsAny(ref.ResourceID, "/\\") {
		return projectError(file, ref.Pointer, "invalid_file_reference", "resourceId must be a bounded opaque identifier")
	}
	for _, character := range ref.ResourceID {
		if character < 0x20 || character == 0x7f || character == ' ' {
			return projectError(file, ref.Pointer, "invalid_file_reference", "resourceId must be a bounded opaque identifier")
		}
	}
	if !validInputFilename(ref.Filename) {
		return projectError(file, ref.Pointer, "invalid_file_reference", "filename must be a safe logical file name")
	}
	if !lowerSHA256.MatchString(ref.SHA256) || ref.SizeBytes < 0 || ref.SizeBytes > projectInputFileBytesLimit {
		return projectError(file, ref.Pointer, "file_reference_invalid", "file reference has invalid frozen size or SHA-256")
	}
	return nil
}

func validInputFilename(filename string) bool {
	if filename == "" || filename == "." || filename == ".." || len(filename) > 255 || !utf8.ValidString(filename) || strings.TrimSpace(filename) != filename || strings.ContainsAny(filename, `/\:`) {
		return false
	}
	for _, character := range filename {
		if unicode.IsControl(character) {
			return false
		}
	}
	if extension := path.Ext(filename); extension != "" && !safeInputExtension.MatchString(extension) {
		return false
	}
	return true
}

func generatedInputPath(hash, filename string) string {
	return ".leafrun-input/" + hash + path.Ext(filename)
}

func prepareProjectFileRefs(file string, loader *projectSchemaLoader, root *jsonschema.Schema, parameters map[string]any, refs []FileRef, alreadyPrepared bool) ([]PreparedFile, error) {
	if len(refs) > projectInputFileCountLimit {
		return nil, projectError(file, "", "file_reference_limit", "at most 64 file references are allowed")
	}
	ordered := slices.Clone(refs)
	slices.SortFunc(ordered, func(left, right FileRef) int { return strings.Compare(left.Pointer, right.Pointer) })
	seenPointer := make(map[string]bool, len(ordered))
	contentSizes := make(map[string]int64, len(ordered))
	digestSizes := make(map[string]int64, len(ordered))
	for _, ref := range ordered {
		if err := validateFileRef(file, ref); err != nil {
			return nil, err
		}
		if seenPointer[ref.Pointer] {
			return nil, projectError(file, ref.Pointer, "duplicate_file_reference", "a parameter pointer can have only one file reference")
		}
		seenPointer[ref.Pointer] = true
		if !schemaPointerIsFile(loader, root, parameters, ref.Pointer) {
			return nil, projectError(file, ref.Pointer, "file_reference_invalid", "pointer does not identify an x-leafrun-file string field")
		}
		generated := generatedInputPath(ref.SHA256, ref.Filename)
		if old, ok := contentSizes[generated]; ok && old != ref.SizeBytes {
			return nil, projectError(file, ref.Pointer, "file_reference_invalid", "one SHA-256 cannot identify different byte lengths")
		}
		if old, ok := digestSizes[ref.SHA256]; ok && old != ref.SizeBytes {
			return nil, projectError(file, ref.Pointer, "file_reference_invalid", "one SHA-256 cannot identify different byte lengths")
		}
		contentSizes[generated] = ref.SizeBytes
		digestSizes[ref.SHA256] = ref.SizeBytes
	}
	var total int64
	for _, size := range contentSizes {
		if size > projectInputFileBytesLimit-total {
			return nil, projectError(file, "", "input_file_bytes_limit", "unique input file bytes exceed 25 MiB")
		}
		total += size
	}
	refsByPointer := make(map[string]FileRef, len(ordered))
	for _, ref := range ordered {
		refsByPointer[ref.Pointer] = ref
	}
	if err := inspectFileParameters(file, loader, parameters, []*jsonschema.Schema{root}, "", refsByPointer, alreadyPrepared); err != nil {
		return nil, err
	}
	prepared := make([]PreparedFile, 0, len(ordered))
	for _, ref := range ordered {
		generated := generatedInputPath(ref.SHA256, ref.Filename)
		if !alreadyPrepared {
			if err := setProjectPointer(parameters, ref.Pointer, "/"+generated); err != nil {
				return nil, projectError(file, ref.Pointer, "file_reference_invalid", "file reference pointer does not resolve to an existing parameter object")
			}
		}
		prepared = append(prepared, PreparedFile{Pointer: ref.Pointer, ResourceID: ref.ResourceID, Filename: ref.Filename, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes, Path: generated})
	}
	return prepared, nil
}

func inspectFileParameters(file string, loader *projectSchemaLoader, value any, schemas []*jsonschema.Schema, pointer string, refs map[string]FileRef, alreadyPrepared bool) error {
	schemas = expandProjectUnconditionalSchemas(schemas)
	if projectSchemaIsFile(loader, schemas) {
		ref, exists := refs[pointer]
		if !exists {
			return projectError(file, pointer, "file_reference_required", "file fields can only be populated through a bound fileRef")
		}
		if !alreadyPrepared {
			// A null array slot is an explicit placeholder for its fileRef;
			// arbitrary strings are never overwritten as though authorized.
			if value != nil {
				return projectError(file, pointer, "file_reference_required", "file fields cannot contain user-supplied paths")
			}
			return nil
		}
		if value != "/"+generatedInputPath(ref.SHA256, ref.Filename) {
			return projectError(file, pointer, "prepared_file_binding_mismatch", "prepared file path does not match its descriptor")
		}
		return nil
	}
	switch instance := value.(type) {
	case map[string]any:
		for _, name := range sortedAnyMapKeys(instance) {
			childSchemas := projectPropertySchemas(schemas, name)
			childPointer := pointer + "/" + escapeJSONPointer(name)
			if err := inspectFileParameters(file, loader, instance[name], childSchemas, childPointer, refs, alreadyPrepared); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range instance {
			childPointer := pointer + "/" + strconv.Itoa(index)
			if err := inspectFileParameters(file, loader, child, projectItemSchemas(schemas, index), childPointer, refs, alreadyPrepared); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaPointerIsFile(loader *projectSchemaLoader, root *jsonschema.Schema, value any, pointer string) bool {
	tokens, err := decodeProjectJSONPointer(pointer)
	if err != nil || len(tokens) == 0 {
		return false
	}
	current := []*jsonschema.Schema{root}
	for _, token := range tokens {
		current = expandProjectUnconditionalSchemas(current)
		// JSON Pointer has no syntax distinction between numeric object keys
		// and array indices. Follow the actual instance shape, including when
		// the schema permits both shapes at the same node.
		switch instance := value.(type) {
		case map[string]any:
			current = projectPropertySchemas(current, token)
			value = instance[token]
		case []any:
			index, parseErr := strconv.Atoi(token)
			if parseErr != nil || index < 0 || index >= len(instance) || strconv.Itoa(index) != token {
				return false
			}
			current = projectItemSchemas(current, index)
			value = instance[index]
		default:
			return false
		}
		if len(current) == 0 {
			return false
		}
	}
	return projectSchemaIsFile(loader, current)
}

func projectPropertySchemas(schemas []*jsonschema.Schema, name string) []*jsonschema.Schema {
	result := []*jsonschema.Schema{}
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		matched := false
		if child := schema.Properties[name]; child != nil {
			result = append(result, child)
			matched = true
		}
		for pattern, child := range schema.PatternProperties {
			if pattern.MatchString(name) {
				result = append(result, child)
				matched = true
			}
		}
		if child, ok := schema.AdditionalProperties.(*jsonschema.Schema); ok && !matched {
			result = append(result, child)
		}
	}
	return dedupeProjectSchemas(result)
}

func projectSchemaIsFile(loader *projectSchemaLoader, schemas []*jsonschema.Schema) bool {
	for _, schema := range expandProjectUnconditionalSchemas(schemas) {
		if raw, ok := projectSchemaRawAtLocation(loader, schema.Location); ok {
			if extension, exists := raw["x-leafrun-file"]; exists && extension == true {
				return true
			}
		}
	}
	return false
}

func projectSchemaRawAtLocation(loader *projectSchemaLoader, location string) (map[string]any, bool) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, false
	}
	resource := *u
	resource.Fragment, resource.RawFragment = "", ""
	name, err := projectSchemaResourceName(resource.String())
	if err != nil {
		return nil, false
	}
	value, exists := loader.resources[name]
	if !exists {
		return nil, false
	}
	if u.Fragment != "" {
		tokens, err := decodeProjectJSONPointer(u.Fragment)
		if err != nil {
			return nil, false
		}
		for _, token := range tokens {
			switch current := value.(type) {
			case map[string]any:
				value, exists = current[token]
				if !exists {
					return nil, false
				}
			case []any:
				index, parseErr := strconv.Atoi(token)
				if parseErr != nil || index < 0 || index >= len(current) || strconv.Itoa(index) != token {
					return nil, false
				}
				value = current[index]
			default:
				return nil, false
			}
		}
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func equalPreparedFiles(left, right []PreparedFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
