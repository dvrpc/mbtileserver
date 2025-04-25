// RefreshHandler supports refreshing vector tiles from PostgreSQL database
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RefreshRequest represents the data sent by the client to request a refresh
type RefreshRequest struct {
	Category    string `json:"category,omitempty"`
	TilesetName string `json:"tilesetName"`
}

// RefreshResponse represents the data returned to the client after a refresh
type RefreshResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TilesetID   string `json:"tilesetId,omitempty"`
	TilesetPath string `json:"tilesetPath,omitempty"`
}

// RefreshHandlerFunc returns a handler function for refreshing tilesets
func (s *ServiceSet) RefreshHandlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only allow POST requests
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Error reading request body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req RefreshRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Error parsing JSON", http.StatusBadRequest)
			return
		}

		// Create context with timeout (20 minutes for potentially large operations)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
		defer cancel()

		// Process the request based on mode
		var response RefreshResponse
		{
			// Process single dataset
			if req.TilesetName == "" || req.Category == "" {
				http.Error(w, "category and tilesetName are required for single refreshes", http.StatusBadRequest)
				return
			}

			response = handleSingleRefresh(ctx, req.Category, req.TilesetName)
		}

		// Return response as JSON
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}

// handleSingleRefresh processes a single dataset
func handleSingleRefresh(ctx context.Context, category, datasetName string) RefreshResponse {
	// Determine output directory
	outputDir := os.Getenv("TILE_DIR")
	if outputDir == "" {
		outputDir = "./tilesets"
	}
	
	// Create temporary directory for processing
	tempDir, err := createTempDir()
	if err != nil {
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error creating temporary directory: %v", err),
		}
	}
	defer os.RemoveAll(tempDir)
	
	// Run the Python script
	cmd := exec.CommandContext(ctx, "./tilemaker/venv/bin/python", "-u", "./tilemaker/tilemaker.py")
	
	// Set environment variables for the script
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CATEGORY=%s", category),
		fmt.Sprintf("DATASET_NAME=%s", datasetName),
		fmt.Sprintf("REFRESH_MODE=single"),
		fmt.Sprintf("TILE_DIR=%s", outputDir),
		fmt.Sprintf("WORKING_DIR=%s", tempDir),
	)
	
	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error refreshing tileset: %v\nOutput: %s", err, string(output)),
		}
	}
	
	// Construct the response
	outputPath := filepath.Join(outputDir, category, fmt.Sprintf("%s.mbtiles", datasetName))
	
	// Check if file exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to create tileset. Script output: %s", string(output)),
		}
	}
	
	return RefreshResponse{
		Success:     true,
		Message:     "Tileset refreshed successfully",
		TilesetID:   fmt.Sprintf("%s/%s", category, datasetName),
		TilesetPath: fmt.Sprintf("/data/%s/%s/tiles/{z}/{x}/{y}.pbf", category, datasetName),
	}
}

// createTempDir creates a temporary directory for processing
func createTempDir() (string, error) {
	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("tilemaker-%d", time.Now().UnixNano()))
	return tempDir, os.MkdirAll(tempDir, 0755)
}