package configs

import _ "embed"

// BuiltinModelCardsYAML is the built-in model card catalog bundled with aicli.
//
//go:embed model_cards.yaml
var BuiltinModelCardsYAML []byte
