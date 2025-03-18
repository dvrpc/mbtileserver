package handlers

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"
)

// TemplateRenderer is a custom template renderer for Echo
type TemplateRenderer struct {
	Templates *template.Template
}

// Render renders a template document
func (t *TemplateRenderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.Templates.ExecuteTemplate(w, name, data)
}

// HomeHandler renders the home page listing all available tilesets
func (s *ServiceSet) HomeHandler(c echo.Context) error {
	tilesets := make([]map[string]interface{}, 0)
	
	// Get all service IDs
	var ids []string
	for id := range s.tilesets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	
	// Construct the base URL for services
	scheme := "http"
	if c.Request().TLS != nil {
		scheme = "https"
	}
	hostName := getRequestHost(c.Request())
	rootURL := fmt.Sprintf("%s://%s%s", scheme, hostName, s.rootURL.Path)
	
	// Add each service's information
	for _, id := range ids {
		ts := s.tilesets[id]
		
		// Use the TileJSON metadata for description
		var description string
		tileJSON, err := ts.TileJSON(hostName, scheme)
		if err == nil {
			if desc, ok := tileJSON["description"].(string); ok && desc != "" {
				description = desc
			}
		}
		
		tileInfo := map[string]interface{}{
			"Name":        ts.name,
			"Description": description,
			"URL":         fmt.Sprintf("%s/%s", rootURL, id),
			"ImageType":   ts.tileFormatString(),
		}
		tilesets = append(tilesets, tileInfo)
	}
	
	return c.Render(http.StatusOK, "home", map[string]interface{}{
		"Tilesets": tilesets,
	})
}