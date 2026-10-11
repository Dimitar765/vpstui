package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"vpstui/internal/models"
)

// TagKey identifies one container for tagging purposes: the host's stable ID
// plus the docker name. A name is used rather than the container id because ids
// are minted anew on every recreate, which is exactly the moment a label has to
// survive.
func TagKey(hostID, name string) string { return hostID + "/" + name }

func tagsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tags.json"), nil
}

// LoadTags reads the label map. A missing file is the normal case, not an
// error: tagging is opt-in.
func LoadTags() (map[string]models.Tag, error) {
	p, err := tagsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var tags map[string]models.Tag
	if err := json.Unmarshal(data, &tags); err != nil {
		renameAside(p)
		return nil, fmt.Errorf("corrupt tags file %s — renamed to %s.bad: %w", p, p, err)
	}
	return tags, nil
}

// SaveTags writes the map. json sorts map keys, so an unchanged set produces
// byte-identical file contents.
func SaveTags(tags map[string]models.Tag) error {
	p, err := tagsPath()
	if err != nil {
		return err
	}
	if tags == nil {
		tags = map[string]models.Tag{}
	}
	return writeJSON(p, tags)
}
