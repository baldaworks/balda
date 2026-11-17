package webui

// LifecycleView contains the shared presentation paths for a resource-owned
// lifecycle projection. Eligibility remains in each feature service.
type LifecycleView struct {
	Selected                         string
	ActivePath, DeletedPath, AllPath string
	ListPath, CreatePath             string
}
