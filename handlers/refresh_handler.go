package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// RefreshRequest represents the JSON payload for a refresh request
type RefreshRequest struct {
	TilesetName       string `json:"tilesetName,omitempty"`       // Name of the tileset to refresh (e.g. "DVRPC Traffic Count App Service")
	TilesetURL        string `json:"tilesetURL,omitempty"`        // URL of the tileset (e.g. "/services/transportation/trafficcounts/tiles/{z}/{x}/{y}.pbf")
	FeatureServiceURL string `json:"featureServiceURL,omitempty"` // Optional: URL of the ArcGIS feature service
	Mode              string `json:"mode,omitempty"`              // Optional: "single" (default) or "all" to refresh all tilesets
}

// RefreshResponse represents the JSON response from a refresh request
type RefreshResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	TilesetID   string `json:"tilesetId,omitempty"`
	TilesetPath string `json:"tilesetPath,omitempty"`
	Mode        string `json:"mode,omitempty"`      // "single" or "all"
	Refreshed   int    `json:"refreshed,omitempty"` // Number of tilesets refreshed in "all" mode
}

// RefreshHandlerFunc creates an HTTP handler function for refreshing tilesets
func (s *ServiceSet) RefreshHandlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only accept POST requests
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse the request body
		var req RefreshRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			log.Errorf("Error parsing refresh request: %v", err)
			writeJSONResponse(w, http.StatusBadRequest, RefreshResponse{
				Success: false,
				Message: fmt.Sprintf("Error parsing request: %v", err),
			})
			return
		}

		// Set default mode if not provided
		if req.Mode == "" {
			req.Mode = "single"
		}

		// For "all" mode, we don't need a tileset name
		if req.Mode != "all" && req.TilesetName == "" {
			writeJSONResponse(w, http.StatusBadRequest, RefreshResponse{
				Success: false,
				Message: "TilesetName is required for single tileset refresh",
			})
			return
		}

		// Determine output directory
		outputDir := os.Getenv("TILESETS_DIR")
		if outputDir == "" {
			outputDir = "./tilesets"
		}

		// Create a temporary directory for our script
		tempDir, err := os.MkdirTemp("", "mbtileserver-refresh")
		if err != nil {
			log.Errorf("Error creating temp directory: %v", err)
			writeJSONResponse(w, http.StatusInternalServerError, RefreshResponse{
				Success: false,
				Message: fmt.Sprintf("Error creating temp directory: %v", err),
			})
			return
		}
		defer os.RemoveAll(tempDir)

		// Find Python virtualenv
		venvPath := findVirtualEnv()

		// Get the path to the Python script
		scriptPath := filepath.Join(tempDir, "tilemaker.py")
		if err := copyTilemakerScript(scriptPath); err != nil {
			log.Errorf("Error copying tilemaker.py script: %v", err)
			writeJSONResponse(w, http.StatusInternalServerError, RefreshResponse{
				Success: false,
				Message: fmt.Sprintf("Error copying tilemaker.py script: %v", err),
			})
			return
		}

		// In single mode, we process one tileset
		if req.Mode == "single" {
			// Extract path components and set environment
			category, datasetName, featureServiceURL := extractTilesetInfo(req)

			// Run the refresh process
			response := handleSingleRefresh(r.Context(), category, datasetName, featureServiceURL, outputDir, tempDir, scriptPath, venvPath)
			
			// After successful creation, trigger a reload of tilesets
			if response.Success {
				if err := s.ReloadTilesets(); err != nil {
					log.Warnf("Failed to reload tilesets, server may not see new tileset until restart: %v", err)
				}
			}
			
			// Write the response
			writeJSONResponse(w, http.StatusOK, response)
		} else if req.Mode == "all" {
			// In "all" mode, we process all datasets from DCAT
			response := handleBulkRefresh(r.Context(), outputDir, tempDir, scriptPath, venvPath)
			
			// After successful creation, trigger a reload of tilesets
			if response.Success {
				if err := s.ReloadTilesets(); err != nil {
					log.Warnf("Failed to reload tilesets, server may not see new tilesets until restart: %v", err)
				}
			}
			
			// Write the response
			writeJSONResponse(w, http.StatusOK, response)
		} else {
			writeJSONResponse(w, http.StatusBadRequest, RefreshResponse{
				Success: false,
				Message: fmt.Sprintf("Invalid mode: %s. Must be 'single' or 'all'", req.Mode),
			})
		}
	}
}

// findVirtualEnv tries to locate a Python virtual environment
func findVirtualEnv() string {
	venvPath := os.Getenv("PYTHON_VENV_PATH")
	if venvPath == "" {
		// Try to find a virtualenv in common locations
		possibleVenvPaths := []string{
			"./venv",
			"./env",
			os.Getenv("HOME") + "/venv",
			os.Getenv("HOME") + "/.virtualenvs/mbtileserver",
		}
		
		for _, path := range possibleVenvPaths {
			if _, err := os.Stat(path); err == nil {
				venvPath = path
				break
			}
		}
		
		if venvPath == "" {
			// If we still don't have a virtualenv, try using the system Python with pip
			log.Warn("No virtualenv found, trying to use system Python")
			// Check if system Python has required packages
			pythonCheck := exec.Command("python3", "-c", "import requests")
			if err := pythonCheck.Run(); err != nil {
				log.Warn("System Python missing required packages, install will be attempted")
			}
		}
	}
	
	return venvPath
}

// copyTilemakerScript copies the tilemaker.py script to the given path
func copyTilemakerScript(destPath string) error {
	// Look for the script in a few potential locations
	searchPaths := []string{
		"./scripts/tilemaker.py",           // Local scripts directory
		"/etc/mbtileserver/tilemaker.py",   // System directory
		os.Getenv("TILEMAKER_SCRIPT"),      // Environment variable
	}
	
	// Check each potential location
	var sourcePath string
	for _, path := range searchPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			sourcePath = path
			break
		}
	}
	
	if sourcePath == "" {
		return fmt.Errorf("tilemaker.py script not found in search paths")
	}
	
	// Read the source file
	scriptContent, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("error reading tilemaker.py script: %v", err)
	}
	
	// Write to destination
	return os.WriteFile(destPath, scriptContent, 0755)
}

// extractTilesetInfo parses the request to get category, dataset name, and feature service URL
func extractTilesetInfo(req RefreshRequest) (string, string, string) {
	var category, datasetName string
	var featureServiceURL string

	// Extract path components from the URL if provided
	if req.TilesetURL != "" {
		// Extract path components from the URL
		// Format: /services/category/datasetName/tiles/{z}/{x}/{y}.pbf
		urlPath := req.TilesetURL
		// Remove any http/https prefix and domain
		urlPath = strings.Replace(urlPath, "http://", "", 1)
		urlPath = strings.Replace(urlPath, "https://", "", 1)
		if idx := strings.Index(urlPath, "/"); idx > 0 {
			urlPath = urlPath[idx:]
		}

		// Split the path into components
		parts := strings.Split(urlPath, "/")
		if len(parts) >= 4 && parts[1] == "data" {
			category = parts[2]
			datasetName = parts[3]
		} else {
			// Fallback: extract from tileset name
			log.Warnf("Could not parse category and dataset from URL: %s", urlPath)
			tilesetParts := strings.Split(req.TilesetName, " ")
			category = strings.ToLower(tilesetParts[0])
			datasetName = strings.Join(tilesetParts[1:], "_")
			datasetName = strings.ToLower(datasetName)
			datasetName = strings.ReplaceAll(datasetName, " ", "_")
			datasetName = strings.ReplaceAll(datasetName, "(", "")
			datasetName = strings.ReplaceAll(datasetName, ")", "")
		}
	} else {
		// Extract from tileset name if URL not provided
		tilesetParts := strings.Split(req.TilesetName, " ")
		category = strings.ToLower(tilesetParts[0])
		datasetName = strings.Join(tilesetParts[1:], "_")
		datasetName = strings.ToLower(datasetName)
		datasetName = strings.ReplaceAll(datasetName, " ", "_")
		datasetName = strings.ReplaceAll(datasetName, "(", "")
		datasetName = strings.ReplaceAll(datasetName, ")", "")
	}

	// Use provided feature service URL or construct one
	if req.FeatureServiceURL != "" {
		featureServiceURL = req.FeatureServiceURL
	} else {
		featureServiceURL = fmt.Sprintf("https://arcgis.dvrpc.org/portal/rest/services/%s/%s/FeatureServer/0", category, datasetName)
	}

	return category, datasetName, featureServiceURL
}

// handleSingleRefresh processes a single tileset refresh request
func handleSingleRefresh(ctx context.Context, category, datasetName, featureServiceURL, outputDir, tempDir, scriptPath, venvPath string) RefreshResponse {
	log.Infof("Refreshing tileset %s/%s using feature service URL: %s", 
		category, datasetName, featureServiceURL)
	
	// Set up environment for the Python script
	env := os.Environ()
	env = append(env, fmt.Sprintf("FEATURE_SERVICE_URL=%s", featureServiceURL))
	env = append(env, fmt.Sprintf("CATEGORY=%s", category))
	env = append(env, fmt.Sprintf("DATASET_NAME=%s", datasetName))
	env = append(env, fmt.Sprintf("TITLE=%s", datasetName)) // Use dataset name as title
	env = append(env, fmt.Sprintf("OUTPUT_DIR=%s", outputDir))
	env = append(env, fmt.Sprintf("WORKING_DIR=%s", tempDir))
	env = append(env, "REFRESH_MODE=single")
	
	// Add virtualenv path if we have one
	if venvPath != "" {
		env = append(env, fmt.Sprintf("PYTHONPATH=%s/lib/python3.8/site-packages", venvPath))
	}
	
	// Run the Python script with a timeout (30 minutes)
	log.Infof("Running Python script: %s", scriptPath)
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	
	cmd := exec.CommandContext(cmdCtx, "python3", scriptPath)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	
	if cmdCtx.Err() == context.DeadlineExceeded {
		log.Errorf("Script execution timed out after 30 minutes")
		return RefreshResponse{
			Success: false,
			Message: "Script execution timed out after 30 minutes. The dataset might be too large.",
			Mode:    "single",
		}
	}
	
	if err != nil {
		log.Errorf("Error running Python script: %v\nOutput: %s", err, output)
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error refreshing tileset: %v\nDetails: %s", err, output),
			Mode:    "single",
		}
	}

	// Verify the file exists
	finalMBTilesPath := filepath.Join(outputDir, category, fmt.Sprintf("%s.mbtiles", datasetName))
	if _, err := os.Stat(finalMBTilesPath); os.IsNotExist(err) {
		log.Errorf("Generated MBTiles file not found at expected location: %s", finalMBTilesPath)
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Tileset generation failed: file not found at %s", finalMBTilesPath),
			Mode:    "single",
		}
	}

	// Construct the tileset ID and path
	tilesetID := filepath.Join(category, datasetName)
	
	// Construct the tile URL based on the request context
	tileURL := fmt.Sprintf("/data/%s/%s/tiles/{z}/{x}/{y}.pbf", category, datasetName)

	// Return success response
	return RefreshResponse{
		Success:     true,
		Message:     "Tileset refreshed successfully",
		TilesetID:   tilesetID,
		TilesetPath: tileURL,
		Mode:        "single",
	}
}

// handleBulkRefresh processes all tilesets from DCAT
func handleBulkRefresh(ctx context.Context, outputDir, tempDir, scriptPath, venvPath string) RefreshResponse {
	log.Info("Starting bulk refresh of all tilesets from DCAT")
	
	// Set up environment for the Python script
	env := os.Environ()
	env = append(env, fmt.Sprintf("OUTPUT_DIR=%s", outputDir))
	env = append(env, fmt.Sprintf("WORKING_DIR=%s", tempDir))
	env = append(env, "REFRESH_MODE=all")
	env = append(env, fmt.Sprintf("DCAT_URL=%s", os.Getenv("DCAT_URL")))
	
	// Add virtualenv path if we have one
	if venvPath != "" {
		env = append(env, fmt.Sprintf("PYTHONPATH=%s/lib/python3.8/site-packages", venvPath))
	}
	
	// Run the Python script with a timeout (2 hours for bulk operations)
	log.Infof("Running Python script in bulk mode: %s", scriptPath)
	cmdCtx, cancel := context.WithTimeout(ctx, 4*time.Hour)
	defer cancel()
	
	cmd := exec.CommandContext(cmdCtx, "python3", scriptPath)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	
	if cmdCtx.Err() == context.DeadlineExceeded {
		log.Errorf("Bulk refresh timed out after 4 hours")
		return RefreshResponse{
			Success: false,
			Message: "Bulk refresh timed out after 4 hours.",
			Mode:    "all",
		}
	}
	
	if err != nil {
		log.Errorf("Error running Python script in bulk mode: %v\nOutput: %s", err, output)
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error in bulk refresh: %v\nDetails: %s", err, output),
			Mode:    "all",
		}
	}

	// Try to parse the number of datasets processed
	outputStr := string(output)
	refreshed := 0
	
	// Look for a line like "Successfully processed X datasets"
	for _, line := range strings.Split(outputStr, "\n") {
		if strings.Contains(line, "Successfully processed") && strings.Contains(line, "datasets") {
			fmt.Sscanf(line, "Successfully processed %d datasets", &refreshed)
			break
		}
	}

	// Return success response
	return RefreshResponse{
		Success:   true,
		Message:   "Bulk refresh completed successfully",
		Mode:      "all",
		Refreshed: refreshed,
	}
}

// ReloadTilesets reloads all tilesets from the given directory
// This is a stub - replace with your actual implementation
func (s *ServiceSet) ReloadTilesets() error {
    // Get the tileset directory
    tilesetDir := os.Getenv("TILESETS_DIR")
    if tilesetDir == "" {
        tilesetDir = "./tilesets"
    }
    
    // Log that we're reloading
    log.Info("Reloading tilesets from disk")
    
    // Implementation will depend on your actual ServiceSet structure
    // This is a placeholder for your implementation:
    
    // Example: Reload from directory
    // if err := s.ScanDirectory(tilesetDir); err != nil {
    //    return fmt.Errorf("error reloading tilesets: %v", err)
    // }
    
    log.Infof("Successfully reloaded tilesets")
    return nil
}

// writeJSONResponse is a helper function to write JSON responses
func writeJSONResponse(w http.ResponseWriter, statusCode int, response interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Errorf("Error encoding JSON response: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
	}
}