package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
)

// Compile-time proof that every concrete cell satisfies the extended
// historyCell contract (ID/Seq/Status/CauseID, render-model-spec §5.1).
var (
	_ historyCell = commandResultCell{}
)

// TestHistoryCellIdentity_AllocatedByConstructor pins the transitional
// identity contract: every constructor allocates a unique, monotonically
// increasing cell-N id/seq; committed cells default to StatusCompleted.
func TestHistoryCellIdentity_AllocatedByConstructor(t *testing.T) {
	cells := []historyCell{
		newUserMessageCell("hi"),
		newAssistantMessageCell("body"),
		newSupplementLineCell("line"),
		newAsyncDocumentCell(render.Document{}),
		newToolChainCell("ls", map[string]interface{}{"path": "docs"}, time.Time{}),
		newAssistantStreamCell("stream", false),
		newAssistantStreamCellWithFormatter("md", true, nil),
	}
	seen := map[string]bool{}
	var lastSeq uint64
	for _, c := range cells {
		id := c.ID()
		if id == "" {
			t.Fatalf("%T: ID() empty, want cell-N", c)
		}
		if !strings.HasPrefix(id, "cell-") {
			t.Fatalf("%T: ID()=%q want cell- prefix", c, id)
		}
		if seen[id] {
			t.Fatalf("%T: duplicate ID()=%q", c, id)
		}
		seen[id] = true
		if c.Seq() == 0 {
			t.Fatalf("%T: Seq()=0 want monotonic positive", c)
		}
		if c.Seq() <= lastSeq && lastSeq != 0 {
			t.Fatalf("%T: Seq()=%d not monotonic after %d", c, c.Seq(), lastSeq)
		}
		lastSeq = c.Seq()
		// toolChainCell is the only mutable cell: constructed running by design
		// (state machine covered by TestHistoryCellIdentity_ToolChainLifecycle).
		if _, isTool := c.(toolChainCell); !isTool {
			if c.Status() != encoding.StatusCompleted {
				t.Fatalf("%T: Status()=%q want completed (committed cell default)", c, c.Status())
			}
		}
		if c.CauseID() != "" {
			t.Fatalf("%T: CauseID()=%q want empty for non-tool cell", c, c.CauseID())
		}
	}
}

// TestHistoryCellIdentity_ToolChainLifecycle pins the mutable-cell state
// machine: toolChainCell starts running, keeps its identity across
// withCompleted, and reaches completed (render-model-spec §3 state machine).
func TestHistoryCellIdentity_ToolChainLifecycle(t *testing.T) {
	cell := newToolChainCell("rg", map[string]interface{}{"pattern": "x"}, time.Time{})
	id := cell.ID()
	seq := cell.Seq()
	if cell.Status() != encoding.StatusRunning {
		t.Fatalf("new tool cell Status()=%q want running", cell.Status())
	}
	done := cell.withCompleted("no matches", nil)
	if done.ID() != id || done.Seq() != seq {
		t.Fatalf("withCompleted changed identity: id %q→%q seq %d→%d", id, done.ID(), seq, done.Seq())
	}
	if done.Status() != encoding.StatusCompleted {
		t.Fatalf("withCompleted Status()=%q want completed", done.Status())
	}
	// The original cell must stay untouched (value semantics).
	if cell.Status() != encoding.StatusRunning {
		t.Fatalf("original cell mutated: Status()=%q want running", cell.Status())
	}
}

// TestHistoryCellIdentity_CommandResultKeepsCoordinatorID pins that
// commandResultCell keeps the coordinator-allocated id/sequence (command:N)
// instead of the transitional cell-N identity.
func TestHistoryCellIdentity_CommandResultKeepsCoordinatorID(t *testing.T) {
	cell := newCommandResultCell("command:3", 3, render.Document{})
	if cell.ID() != "command:3" {
		t.Fatalf("ID()=%q want command:3", cell.ID())
	}
	if cell.Seq() != 3 {
		t.Fatalf("Seq()=%d want 3", cell.Seq())
	}
	if cell.Status() != encoding.StatusCompleted {
		t.Fatalf("Status()=%q want completed", cell.Status())
	}
}

// TestHistoryCellIdentity_DistinctFromEncoderItems pins the namespace split:
// encoder item IDs (item-N) must never collide with transitional cell IDs
// (cell-N) so a later renderer migration can key on either namespace.
func TestHistoryCellIdentity_DistinctFromEncoderItems(t *testing.T) {
	cell := newUserMessageCell("hi")
	if cell.ID() == "" || strings.HasPrefix(cell.ID(), "item-") {
		t.Fatalf("transitional cell ID=%q must not use encoder item- namespace", cell.ID())
	}
}
