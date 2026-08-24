// Package skill embeds and installs the portable Artisan agent skills.
package skill

//go:generate go run ./cmd/embedskill ../../skills content_generated.go

const (
	DefaultName = "artisan-inventory"
	// Name preserves the original inventory-skill package surface.
	Name = DefaultName
)

// Content preserves the original inventory-skill content snapshot. Registry
// lookup and installation use the immutable generated constants directly.
var Content = []byte(generatedContentArtisanInventory)

// Definition is an owned snapshot of one embedded skill.
type Definition struct {
	Name    string
	Content []byte
}

// Names returns embedded skill names in stable lexical order.
func Names() []string {
	return []string{"artisan-inventory", "artisan-roast-review"}
}

// Lookup returns an owned definition that callers may mutate without changing
// the embedded registry.
func Lookup(name string) (Definition, bool) {
	var contents string
	switch name {
	case "artisan-inventory":
		contents = generatedContentArtisanInventory
	case "artisan-roast-review":
		contents = generatedContentArtisanRoastReview
	default:
		return Definition{}, false
	}
	return Definition{Name: name, Content: []byte(contents)}, true
}
