package loop

import (
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestLegacyOwnerTagParsesAsOwner(t *testing.T) {
	lines := parseQuestionLines("[fact] 仓库测试命令是什么\n[experiment] 缓存能不能撑住\n[decision] 要不要拆包\n[owner] 是否继续\n[otto] 旧标签\n[OTTO] 另一题\n")
	got := map[string]model.OpenQuestionType{}
	for _, line := range lines {
		if line.kind != "add" {
			t.Fatalf("kind %s", line.kind)
		}
		got[dedupKey(line.text)] = line.qtype
	}
	if got["仓库测试命令是什么"] != model.OQFact || got["缓存能不能撑住"] != model.OQExperiment || got["要不要拆包"] != model.OQDecision {
		t.Fatalf("other types changed: %+v", got)
	}
	for _, text := range []string{"是否继续", "旧标签", "另一题"} {
		if got[text] != model.OQOwner {
			t.Fatalf("%s type %s", text, got[text])
		}
	}
	if blocksFreezable(model.OpenQuestion{Type: model.OQOwner, Status: model.OQOpen}) {
		t.Fatal("open owner question blocked freezable")
	}
	if !blocksFreezable(model.OpenQuestion{Type: model.OQFact, Status: model.OQOpen}) {
		t.Fatal("open fact question did not block freezable")
	}
}
