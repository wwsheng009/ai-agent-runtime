package modelcard

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 模型卡片目录的分层合并
//
// sources 按调用方传入顺序从低到高分层（0 = 最低层，通常是内嵌内置目录；
// 随后依次为配置 builtin_path、用户 ~/.aicli/model_cards.yaml、工作区
// ./.aicli/model_cards.yaml、请求级 --model-cards）。每层先解析为原始 YAML
// 映射，再按身份做维度化合并：
//
//   - provider_templates：身份 = id（大小写不敏感）。同 id 字段级合并，
//     高层只覆盖它显式写的字段，低层独有字段保留（高层"加补丁"语义）。
//   - cards：身份 = id。同 id 同样字段级合并，capability / match /
//     native_tools 等子树递归合并；低层未写的字段由高层补，高层写的字段
//     覆盖低层。
//   - 数组与标量：高层整体替换，不做拼接。列表字段（input_modalities、
//     reasoning_efforts、model_ids、protocols…）必须能被高层显式换掉，
//     不能把两层的能力列表累加成更大集合。
//   - 不同 id 的条目只做集合并集，不做身份覆盖：匹配阶段仍按
//     priority → 匹配分 → 层序 排序后逐字段补齐（见 sortMatchedCards）。
//
// 每个条目记录贡献过它的最高层序号（layerRank），用于同优先级/同匹配分
// 卡片之间的层序兜底：工作区 > 用户 > builtin_path > 内置。
// ---------------------------------------------------------------------------

type layerItem struct {
	// key 是规范化（小写、去空白）后的 id；空串表示条目缺失 id，交由最终
	// Validate 报错。
	key   string
	value map[string]interface{}
	rank  int
}

type layerCatalog struct {
	templates     []*layerItem
	cards         []*layerItem
	templateIndex map[string]*layerItem
	cardIndex     map[string]*layerItem
}

type rawLayerSource struct {
	Version           int                      `yaml:"version"`
	ProviderTemplates []map[string]interface{} `yaml:"provider_templates"`
	Cards             []map[string]interface{} `yaml:"cards"`
}

func newLayerCatalog() *layerCatalog {
	return &layerCatalog{
		templateIndex: make(map[string]*layerItem),
		cardIndex:     make(map[string]*layerItem),
	}
}

func parseLayerSource(name string, data []byte) (*rawLayerSource, error) {
	doc := &rawLayerSource{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, fmt.Errorf("parse model card catalog %s: %w", name, err)
	}
	return doc, nil
}

func (l *layerCatalog) clone() *layerCatalog {
	out := newLayerCatalog()
	for _, item := range l.templates {
		copied := &layerItem{key: item.key, value: cloneLayerMap(item.value), rank: item.rank}
		out.templates = append(out.templates, copied)
		if copied.key != "" {
			out.templateIndex[copied.key] = copied
		}
	}
	for _, item := range l.cards {
		copied := &layerItem{key: item.key, value: cloneLayerMap(item.value), rank: item.rank}
		out.cards = append(out.cards, copied)
		if copied.key != "" {
			out.cardIndex[copied.key] = copied
		}
	}
	return out
}

// mergeSource 把一层原始目录并入当前累积结果。同一层内部出现重复 id 时
// 直接报错（复制粘贴错误应在作者层发现，而不是被静默合并掉）。
func (l *layerCatalog) mergeSource(doc *rawLayerSource, rank int) error {
	if err := checkLayerDuplicateIDs(doc.ProviderTemplates, "provider template"); err != nil {
		return err
	}
	if err := checkLayerDuplicateIDs(doc.Cards, "card"); err != nil {
		return err
	}
	mergeLayerItems(&l.templates, l.templateIndex, doc.ProviderTemplates, rank)
	mergeLayerItems(&l.cards, l.cardIndex, doc.Cards, rank)
	return nil
}

func checkLayerDuplicateIDs(entries []map[string]interface{}, kind string) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key := layerItemKey(entry)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate %s id %q", kind, strings.TrimSpace(layerStringValue(entry, "id")))
		}
		seen[key] = struct{}{}
	}
	return nil
}

func mergeLayerItems(items *[]*layerItem, index map[string]*layerItem, entries []map[string]interface{}, rank int) {
	for _, entry := range entries {
		key := layerItemKey(entry)
		if key == "" {
			*items = append(*items, &layerItem{value: cloneLayerMap(entry), rank: rank})
			continue
		}
		if existing, ok := index[key]; ok {
			existing.value = deepMergeLayerMaps(existing.value, entry)
			if rank > existing.rank {
				existing.rank = rank
			}
			continue
		}
		item := &layerItem{key: key, value: cloneLayerMap(entry), rank: rank}
		*items = append(*items, item)
		index[key] = item
	}
}

// catalog 把累积的原始映射重组为 Catalog 并做一次最终校验；失败时调用方
// 回滚该层，保证"坏层不污染已合并结果"。
func (l *layerCatalog) catalog() (*Catalog, error) {
	doc := map[string]interface{}{"version": 1}
	if len(l.templates) > 0 {
		list := make([]interface{}, 0, len(l.templates))
		for _, item := range l.templates {
			list = append(list, item.value)
		}
		doc["provider_templates"] = list
	}
	if len(l.cards) > 0 {
		list := make([]interface{}, 0, len(l.cards))
		for _, item := range l.cards {
			list = append(list, item.value)
		}
		doc["cards"] = list
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode merged model card catalog: %w", err)
	}
	catalog := &Catalog{}
	if err := yaml.Unmarshal(data, catalog); err != nil {
		return nil, fmt.Errorf("decode merged model card catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	for index := range catalog.Cards {
		key := strings.ToLower(strings.TrimSpace(catalog.Cards[index].ID))
		if item, ok := l.cardIndex[key]; ok {
			catalog.Cards[index].layerRank = item.rank
		}
	}
	return catalog, nil
}

func layerItemKey(entry map[string]interface{}) string {
	return strings.ToLower(strings.TrimSpace(layerStringValue(entry, "id")))
}

func layerStringValue(entry map[string]interface{}, field string) string {
	value, ok := entry[field]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// deepMergeLayerMaps 递归合并两个 YAML 映射：两边都是映射时递归；否则高层
// （over）整体替换（含数组与标量）。
func deepMergeLayerMaps(base, over map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(base)+len(over))
	for key, value := range base {
		merged[key] = cloneLayerValue(value)
	}
	for key, overValue := range over {
		baseValue, exists := merged[key]
		if !exists {
			merged[key] = cloneLayerValue(overValue)
			continue
		}
		baseMap, baseOK := baseValue.(map[string]interface{})
		overMap, overOK := overValue.(map[string]interface{})
		if baseOK && overOK {
			merged[key] = deepMergeLayerMaps(baseMap, overMap)
			continue
		}
		merged[key] = cloneLayerValue(overValue)
	}
	return merged
}

func cloneLayerMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	return cloneLayerValue(input).(map[string]interface{})
}

func cloneLayerValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = cloneLayerValue(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, cloneLayerValue(item))
		}
		return out
	default:
		return value
	}
}
