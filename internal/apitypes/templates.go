package apitypes

// Template is one entry of the one-click catalog (GET /api/templates).
// Type is "image" or "compose"; Port is the published UI port used for
// proxy labels (0 when the template is headless).
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Logo        string `json:"logo"`
	Type        string `json:"type"`
	Port        int    `json:"port"`
}
