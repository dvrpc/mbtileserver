package handlers

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strings"

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

// InitTemplates initializes templates with custom functions
func InitTemplates(templatesPath string) *template.Template {
	// Create function map with custom functions
	funcMap := template.FuncMap{
		"contains": strings.Contains,
		"eq":       func(a, b interface{}) bool { return a == b },
	}

	// Parse templates with the function map
	tmpl := template.New("").Funcs(funcMap)
	return template.Must(tmpl.ParseGlob(templatesPath))
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
	
	// Categorize tilesets by URL path
	categories := make(map[string][]map[string]interface{})
	
	for _, tileset := range tilesets {
		url := tileset["URL"].(string)
		category := "Project Specific" // Default category
		
		// Determine category from URL
		if strings.Contains(url, "/boundaries/") {
			category = "Boundaries"
		} else if strings.Contains(url, "/transportation/") {
			category = "Transportation"
		} else if strings.Contains(url, "/demographics/") {
			category = "Demographics"
		} else if strings.Contains(url, "/environment/") {
			category = "Environment"
		} else if strings.Contains(url, "/freight/") {
			category = "Freight"
		} else if strings.Contains(url, "/structures/") {
			category = "Structures"
		} else if strings.Contains(url, "/imagery/") {
			category = "Imagery"		
		} else if strings.Contains(url, "/planning/") {
			category = "Planning"
		}
		
		// Add to the appropriate category
		categories[category] = append(categories[category], tileset)
	}
	
	// Return both the flat list and categorized tilesets
	return c.Render(http.StatusOK, "home", map[string]interface{}{
		"Tilesets":   tilesets,
		"Categories": categories,
	})
}