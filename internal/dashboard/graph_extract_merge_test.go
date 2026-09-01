package dashboard

import (
	"reflect"
	"testing"
)

func TestMergedGraphExtractionFiltersNoiseAndNormalizes(t *testing.T) {
	content := "# １. Ｂｏｏｔｓｔｒａｐ\r\n\r\n" +
		"[[知识图谱]] 使用 `ＫｎｏｗｌｅｄｇｅＳｅｒｖｉｃｅ`。\r\n" +
		"`dict[str, Any`、`JSONDecodeError`、服务、模型、模块和数据库。\r\n" +
		"If\r\nIn\r\nNode\r\nEND\r\ntextContent\r\nEXISTS\r\n" +
		"TEXT NOT NULL\r\nArgumentParser\r\nIt\r\nThis\r\nEvery\r\n" +
		"Local\r\nSoftware\r\nSystemExit\r\nINTEGER NOT NULL\r\nTEXT PRIMARY KEY\r\n"

	entities, relations := extractGraphKnowledge(content, 0)
	labels := make(map[string]bool, len(entities))
	for _, entity := range entities {
		labels[entity.Label] = true
	}
	for _, required := range []string{"Bootstrap", "知识图谱", "KnowledgeService"} {
		if !labels[required] {
			t.Errorf("missing normalized label %q in %#v", required, labels)
		}
	}
	for _, rejected := range []string{
		"1. Bootstrap", "dict[str, Any", "JSONDecodeError", "服务", "模型", "模块", "数据库",
		"If", "In", "Node", "END", "textContent", "EXISTS", "TEXT NOT NULL",
		"ArgumentParser", "It", "This", "Every", "Local", "Software", "SystemExit",
		"INTEGER NOT NULL", "TEXT PRIMARY KEY",
	} {
		if labels[rejected] {
			t.Errorf("unexpected noisy label %q in %#v", rejected, labels)
		}
	}
	if len(relations) != 1 {
		t.Fatalf("relations = %#v, want one relation", relations)
	}
	if relation := relations[0]; relation.Source != "知识图谱" ||
		relation.Target != "KnowledgeService" || relation.Type != "uses" ||
		relation.Line != 3 {
		t.Fatalf("relation = %#v", relation)
	}
	if validGraphLabel("这是一个超过十二个中文字符的知识图谱", "tech") {
		t.Fatal("overlong technical label was accepted")
	}
}

func TestMergedGraphExtractionPreservesOffsetCRLFAndOrder(t *testing.T) {
	content := "[[知识图谱]] 使用 [[语义搜索]]。\r\n"
	firstEntities, firstRelations := extractGraphKnowledge(content, 11)
	secondEntities, secondRelations := extractGraphKnowledge(content, 11)

	if !reflect.DeepEqual(firstEntities, secondEntities) ||
		!reflect.DeepEqual(firstRelations, secondRelations) {
		t.Fatal("graph extraction order is not deterministic")
	}
	if len(firstEntities) != 2 {
		t.Fatalf("entities = %#v, want two", firstEntities)
	}
	if firstEntities[0].Label != "知识图谱" || firstEntities[1].Label != "语义搜索" {
		t.Fatalf("entity order = %#v", firstEntities)
	}
	for _, entity := range firstEntities {
		if entity.Line != 12 {
			t.Fatalf("entity line = %d, want 12", entity.Line)
		}
		if len(entity.Excerpt) > 0 && entity.Excerpt[len(entity.Excerpt)-1] == '\r' {
			t.Fatalf("entity excerpt contains CR: %q", entity.Excerpt)
		}
	}
	if len(firstRelations) != 1 || firstRelations[0].Line != 12 {
		t.Fatalf("relations = %#v, want one relation on line 12", firstRelations)
	}
	if firstRelations[0].Excerpt != "[[知识图谱]] 使用 [[语义搜索]]。" {
		t.Fatalf("relation excerpt = %q", firstRelations[0].Excerpt)
	}
}
