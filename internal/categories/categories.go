// Package categories contains the standard newznab/Torznab category tree used
// by the caps response and by the web filters.
package categories

import (
	"strconv"
	"strings"
)

// Category is one node of the standard tree.
type Category struct {
	ID   int
	Name string
}

// All is the standard category list, in the order used by the UI.
var All = []Category{
	{1000, "Console"},
	{1010, "Console/NDS"},
	{1020, "Console/PSP"},
	{1030, "Console/Wii"},
	{1040, "Console/Xbox"},
	{1050, "Console/Xbox 360"},
	{1060, "Console/Wiiware"},
	{1070, "Console/Xbox 360 DLC"},
	{1080, "Console/PS3"},
	{1110, "Console/3DS"},
	{1120, "Console/PS Vita"},
	{1130, "Console/WiiU"},
	{1140, "Console/Xbox One"},
	{1180, "Console/PS4"},
	{2000, "Movies"},
	{2010, "Movies/Foreign"},
	{2020, "Movies/Other"},
	{2030, "Movies/SD"},
	{2040, "Movies/HD"},
	{2045, "Movies/UHD"},
	{2050, "Movies/BluRay"},
	{2060, "Movies/3D"},
	{2070, "Movies/DVD"},
	{2080, "Movies/WEB-DL"},
	{3000, "Audio"},
	{3010, "Audio/MP3"},
	{3020, "Audio/Video"},
	{3030, "Audio/Audiobook"},
	{3040, "Audio/Lossless"},
	{3050, "Audio/Other"},
	{3060, "Audio/Foreign"},
	{4000, "PC"},
	{4010, "PC/0day"},
	{4020, "PC/ISO"},
	{4030, "PC/Mac"},
	{4040, "PC/Mobile-Other"},
	{4050, "PC/Games"},
	{4060, "PC/Mobile-iOS"},
	{4070, "PC/Mobile-Android"},
	{5000, "TV"},
	{5010, "TV/WEB-DL"},
	{5020, "TV/Foreign"},
	{5030, "TV/SD"},
	{5040, "TV/HD"},
	{5045, "TV/UHD"},
	{5050, "TV/Other"},
	{5060, "TV/Sport"},
	{5070, "TV/Anime"},
	{5080, "TV/Documentary"},
	{5090, "TV/x265"},
	{6000, "XXX"},
	{6010, "XXX/DVD"},
	{6020, "XXX/WMV"},
	{6030, "XXX/XviD"},
	{6040, "XXX/x264"},
	{6045, "XXX/UHD"},
	{6050, "XXX/Pack"},
	{6060, "XXX/ImageSet"},
	{6070, "XXX/Other"},
	{7000, "Books"},
	{7010, "Books/Mags"},
	{7020, "Books/EBook"},
	{7030, "Books/Comics"},
	{7040, "Books/Technical"},
	{7050, "Books/Other"},
	{7060, "Books/Foreign"},
	{8000, "Other"},
	{8010, "Other/Misc"},
	{8020, "Other/Hashed"},
}

var byID = func() map[int]Category {
	out := make(map[int]Category, len(All))
	for _, category := range All {
		out[category.ID] = category
	}
	return out
}()

// Name returns the human-readable name of a category id.
func Name(id string) string {
	value, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil {
		return id
	}
	if category, ok := byID[value]; ok {
		return category.Name
	}
	return "Category " + strconv.Itoa(value)
}

// Get returns a category by id.
func Get(id int) (Category, bool) {
	category, ok := byID[id]
	return category, ok
}

// Parent returns the top-level category of an id (0 for a top-level id).
func Parent(id int) int {
	if id%1000 == 0 {
		return 0
	}
	return id - id%1000
}

// Groups returns the top-level categories, each followed by its children.
type Group struct {
	Top      Category
	Children []Category
}

// Groups builds the tree used by the caps response and the filters.
func Groups() []Group {
	var groups []Group
	for _, category := range All {
		if Parent(category.ID) == 0 {
			groups = append(groups, Group{Top: category})
		}
	}
	for i := range groups {
		for _, category := range All {
			if Parent(category.ID) == groups[i].Top.ID {
				groups[i].Children = append(groups[i].Children, category)
			}
		}
	}
	return groups
}
