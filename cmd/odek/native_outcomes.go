package main

import "fmt"

// nativeToolOutcome is implemented only by native result types. It expresses
// operational status independently of arbitrary returned file/content text.
type nativeToolOutcome interface{ nativeError() string }

func (r readFileResult) nativeError() string    { return r.Error }
func (r writeFileResult) nativeError() string   { return r.Error }
func (r searchFilesResult) nativeError() string { return r.Error }
func (r patchResult) nativeError() string       { return r.Error }
func (r globResult) nativeError() string        { return r.Error }
func (r fileInfoResult) nativeError() string    { return r.Error }
func (r mathEvalResult) nativeError() string    { return r.Error }
func (r diffResult) nativeError() string        { return r.Error }
func (r jsonQueryResult) nativeError() string   { return r.Error }
func (r treeResult) nativeError() string        { return r.Error }
func (r base64Result) nativeError() string      { return r.Error }
func (r trResult) nativeError() string          { return r.Error }
func (r batchReadResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r batchPatchResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r parallelShellResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r httpBatchResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r countLinesResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r multiGrepResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r checksumResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r sortResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r headTailResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}
func (r wordCountResult) nativeError() string {
	failed := 0
	for _, entry := range r.Results {
		if entry.Error != "" {
			failed++
		}
	}
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d operations failed", failed, len(r.Results))
}

func (r visionResult) nativeError() string    { return r.Error }
func (r webSearchOutput) nativeError() string { return r.Error }

func (r browserResult) nativeError() string    { return r.Error }
func (r transcribeResult) nativeError() string { return r.Error }
