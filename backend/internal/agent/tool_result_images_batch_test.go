package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestCollectImagePassthroughsReadsBatchItems: a files[] batch result declares
// its image attachments inside items[]; the injection side must walk them
// (analysis §3.11), dedupe against top-level declarations and skip missing
// paths.
func TestCollectImagePassthroughsReadsBatchItems(t *testing.T) {
	dir := t.TempDir()
	// 名字刻意让"声明顺序"与"字典序"相反：排序实现会把 paths 换成另一个
	// 顺序并让 notes 与路径脱钩（review Q5）。
	batchImage := filepath.Join(dir, "zeta-item.png")
	if err := os.WriteFile(batchImage, []byte("png"), 0o644); err != nil {
		t.Fatalf("seed batch image: %v", err)
	}
	topImage := filepath.Join(dir, "alpha-top.png")
	if err := os.WriteFile(topImage, []byte("png"), 0o644); err != nil {
		t.Fatalf("seed top image: %v", err)
	}

	results := []ToolResultPayload{
		{
			Metadata: types.Metadata{
				"items": []map[string]interface{}{
					{
						toolresult.MetadataImagePassthroughKey: true,
						toolresult.MetadataImagePathKey:        batchImage,
						toolresult.MetadataImageNoteKey:        "已压缩 100x100 → 50x50；显示坐标横向 ×2.00、纵向 ×2.02 得到原图坐标",
					},
					{"file_path": "notes.txt"},
					{
						toolresult.MetadataImagePassthroughKey: true,
						toolresult.MetadataImagePathKey:        filepath.Join(dir, "missing.png"),
					},
				},
			},
		},
		{
			Metadata: types.Metadata{
				toolresult.MetadataImagePassthroughKey: true,
				toolresult.MetadataImagePathKey:        topImage,
			},
		},
		{
			Metadata: types.Metadata{
				// Same path as the batch item: must be deduplicated.
				toolresult.MetadataImagePassthroughKey: true,
				toolresult.MetadataImagePathKey:        batchImage,
			},
		},
	}

	paths, notes := collectImagePassthroughs(results)
	if len(paths) != 2 {
		t.Fatalf("expected the batch image and the top-level image, got %#v", paths)
	}
	if paths[0] != batchImage || paths[1] != topImage {
		t.Fatalf("expected declaration order (batch item first), got %#v", paths)
	}
	if len(notes) != 1 || notes[0] != "已压缩 100x100 → 50x50；显示坐标横向 ×2.00、纵向 ×2.02 得到原图坐标" {
		t.Fatalf("expected the batch image note to stay paired, got %#v", notes)
	}
}

// TestCollectImagePassthroughsFromDecodedItems covers the JSON round-trip
// shape ([]interface{} of maps) that reaches the injection side after a
// history replay.
func TestCollectImagePassthroughsFromDecodedItems(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "replayed.png")
	if err := os.WriteFile(imagePath, []byte("png"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	results := []ToolResultPayload{
		{
			Metadata: types.Metadata{
				"items": []interface{}{
					map[string]interface{}{
						toolresult.MetadataImagePassthroughKey: true,
						toolresult.MetadataImagePathKey:        imagePath,
					},
				},
			},
		},
	}
	paths, _ := collectImagePassthroughs(results)
	if len(paths) != 1 || paths[0] != imagePath {
		t.Fatalf("expected the decoded item image, got %#v", paths)
	}
}

// TestCollectImagePassthroughsReadsExtraPaths: one result may carry several
// images (notebook outputs), declared via the additive image_paths list; the
// primary path stays first and duplicates collapse.
func TestCollectImagePassthroughsReadsExtraPaths(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.png")
	second := filepath.Join(dir, "second.png")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}
	results := []ToolResultPayload{
		{
			Metadata: types.Metadata{
				toolresult.MetadataImagePassthroughKey: true,
				toolresult.MetadataImagePathKey:        first,
				toolresult.MetadataImageNoteKey:        "notebook 图片输出",
				toolresult.MetadataImagePathsKey:       []string{first, second},
			},
		},
	}
	paths, notes := collectImagePassthroughs(results)
	if len(paths) != 2 || paths[0] != first || paths[1] != second {
		t.Fatalf("expected both images in declaration order, got %#v", paths)
	}
	if len(notes) != 1 {
		t.Fatalf("expected the primary note only, got %#v", notes)
	}
}
