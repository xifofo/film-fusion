package embyplayback

var defaultStore = NewStore()

func Default() *Store {
	return defaultStore
}
