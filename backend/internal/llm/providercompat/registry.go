package providercompat

var registeredAdapters = []Adapter{
	sensenovaOpenAIAdapter{},
	nvidiaOpenAIAdapter{},
	deepSeekOpenAIAdapter{},
	chatGPTCodexBackendAdapter{},
	codexPathAdapter{},
	codexDefaultAdapter{},
	openAIDefaultAdapter{},
}

func adaptersForContext(ctx Context) []Adapter {
	adapters := make([]Adapter, 0, len(registeredAdapters))
	for _, adapter := range registeredAdapters {
		if adapter.Match(ctx) {
			adapters = append(adapters, adapter)
		}
	}
	return adapters
}

func adapterAlreadySelected(adapters []Adapter, name string) bool {
	for _, adapter := range adapters {
		if adapter.Name() == name {
			return true
		}
	}
	return false
}
