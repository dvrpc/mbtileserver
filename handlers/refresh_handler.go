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

		// In single mode, we process one tileset
		if req.Mode == "single" {
			// Extract path components and set environment
			category, datasetName, featureServiceURL := extractTilesetInfo(req)

			// Run the refresh process
			response := handleSingleRefresh(r.Context(), w, category, datasetName, featureServiceURL, outputDir, tempDir, venvPath)
			
			// Write the response
			writeJSONResponse(w, http.StatusOK, response)
		} else if req.Mode == "all" {
			// In "all" mode, we process all datasets from DCAT
			response := handleBulkRefresh(r.Context(), w, outputDir, tempDir, venvPath)
			
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
		if len(parts) >= 4 && parts[1] == "services" {
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
func handleSingleRefresh(ctx context.Context, w http.ResponseWriter, category, datasetName, featureServiceURL, outputDir, tempDir, venvPath string) RefreshResponse {
	log.Infof("Refreshing tileset %s/%s using feature service URL: %s", 
		category, datasetName, featureServiceURL)

	// Create the Python script
	scriptPath := filepath.Join(tempDir, "tilemaker.py")
	err := writePythonScript(scriptPath)
	if err != nil {
		log.Errorf("Error creating Python script: %v", err)
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error creating Python script: %v", err),
		}
	}
	
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
	
	// Run the Python script with a timeout (15 minutes)
	log.Infof("Running Python script: %s", scriptPath)
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	
	cmd := exec.CommandContext(cmdCtx, "python3", scriptPath)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	
	if cmdCtx.Err() == context.DeadlineExceeded {
		log.Errorf("Script execution timed out after 15 minutes")
		return RefreshResponse{
			Success: false,
			Message: "Script execution timed out after 15 minutes. The dataset might be too large.",
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

	// Construct the tileset ID and path
	tilesetID := filepath.Join(category, datasetName)
	
	// Construct the tile URL based on the request context
	tileURL := fmt.Sprintf("/services/%s/tiles/{z}/{x}/{y}.pbf", tilesetID)

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
func handleBulkRefresh(ctx context.Context, w http.ResponseWriter, outputDir, tempDir, venvPath string) RefreshResponse {
	log.Info("Starting bulk refresh of all tilesets from DCAT")

	// Create the Python script
	scriptPath := filepath.Join(tempDir, "tilemaker.py")
	err := writePythonScript(scriptPath)
	if err != nil {
		log.Errorf("Error creating Python script: %v", err)
		return RefreshResponse{
			Success: false,
			Message: fmt.Sprintf("Error creating Python script: %v", err),
		}
	}
	
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
	cmdCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	
	cmd := exec.CommandContext(cmdCtx, "python3", scriptPath)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	
	if cmdCtx.Err() == context.DeadlineExceeded {
		log.Errorf("Bulk refresh timed out after 2 hours")
		return RefreshResponse{
			Success: false,
			Message: "Bulk refresh timed out after 2 hours.",
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

// writePythonScript writes the enhanced tilemaker Python script to the given path
func writePythonScript(scriptPath string) error {
	// This is the content of our enhanced tilemaker.py script
	scriptContent := `"""
Enhanced script to download data from ArcGIS Feature Services and create mbtiles using Tippecanoe.
This version supports both single tileset refresh and bulk processing with DCAT metadata.
"""

import json
import os
import subprocess
import requests
import time
import logging
import sys
from pathlib import Path

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger('tilemaker')

# Configuration
DCAT_URL = os.environ.get("DCAT_URL", "https://arcgis.dvrpc.org/api/dcat.json")
CHUNK_SIZE = 2000  # For API calls

# Get configuration from environment variables
FEATURE_SERVICE_URL = os.environ.get("FEATURE_SERVICE_URL")
CATEGORY = os.environ.get("CATEGORY")
DATASET_NAME = os.environ.get("DATASET_NAME")
TITLE = os.environ.get("TITLE", DATASET_NAME)
DESCRIPTION = os.environ.get("DESCRIPTION", "")
ATTRIBUTION = os.environ.get("ATTRIBUTION", "")
OUTPUT_DIR = os.environ.get("OUTPUT_DIR", "./tilesets")
WORKING_DIR = os.environ.get("WORKING_DIR", "/tmp/tilemaker")
REFRESH_MODE = os.environ.get("REFRESH_MODE", "single")  # "single" or "all"

def get_dataset_info_from_url(url):
    """Extract both category and name from ArcGIS REST URL"""
    parts = url.split('/')
    if 'services' in parts:
        services_index = parts.index('services')
        if services_index + 2 < len(parts):
            category = parts[services_index + 1]
            dataset_name = parts[services_index + 2]
            return {
                'category': category,
                'name': dataset_name
            }
    
    # Fallback
    for i, part in enumerate(parts):
        if part == "FeatureServer" and i > 0:
            dataset_name = parts[i-1]
            category = parts[i-2] if i > 1 else "uncategorized"
            return {
                'category': category,
                'name': dataset_name
            }
            
    return {
        'category': "uncategorized",
        'name': None
    }

def download_from_feature_service(service_url, output_file):
    """Download data from an ArcGIS Feature Service in chunks and save as GeoJSON"""
    # Get the count of features first
    count_url = f"{service_url}/query?where=1=1&returnCountOnly=true&f=json"
    logger.info(f"Getting feature count from {count_url}")
    try:
        response = requests.get(count_url, timeout=60)
        if not response.ok:
            logger.error(f"Error getting feature count: {response.status_code} {response.text}")
            return False
        
        count_data = response.json()
        total_features = count_data.get('count', 0)
        
        if total_features == 0:
            logger.warning("No features found in service")
            return False
        
        logger.info(f"Total features to download: {total_features}")
    except requests.RequestException as e:
        logger.error(f"Request error getting feature count: {e}")
        return False
    except Exception as e:
        logger.error(f"Unexpected error getting feature count: {e}")
        return False
    
    # Create GeoJSON structure
    geojson = {
        "type": "FeatureCollection",
        "features": []
    }
    
    # Download in chunks
    offset = 0
    while offset < total_features:
        logger.info(f"Downloading features {offset} to {offset + CHUNK_SIZE}...")
        
        # Query for a chunk of features
        query_url = (
            f"{service_url}/query?"
            f"where=1=1&"
            f"outFields=*&"
            f"returnGeometry=true&"
            f"outSR=4326&"  # Request data in WGS84
            f"resultOffset={offset}&"
            f"resultRecordCount={CHUNK_SIZE}&"
            f"f=geojson"
        )
        
        try:
            response = requests.get(query_url, timeout=120)  # Longer timeout for data requests
            if not response.ok:
                logger.error(f"Error downloading chunk: {response.status_code} {response.text}")
                if offset > 0:
                    # If we've already downloaded some data, we'll continue with what we have
                    break
                return False
            
            chunk_data = response.json()
            chunk_features = chunk_data.get('features', [])
            
            if not chunk_features:
                logger.info("No more features to download")
                break
            
            # Add features to our collection
            geojson['features'].extend(chunk_features)
            
            # Update offset for next chunk
            offset += len(chunk_features)
            
            # Be nice to the server
            time.sleep(0.5)
        except requests.RequestException as e:
            logger.error(f"Request error downloading chunk at offset {offset}: {e}")
            if offset > 0:
                break
            return False
        except Exception as e:
            logger.error(f"Unexpected error downloading chunk at offset {offset}: {e}")
            if offset > 0:
                break
            return False
    
    # Save the combined GeoJSON
    try:
        with open(output_file, 'w') as f:
            json.dump(geojson, f)
        
        logger.info(f"Downloaded {len(geojson['features'])} features to {output_file}")
        return True
    except Exception as e:
        logger.error(f"Error saving GeoJSON to {output_file}: {e}")
        return False

def get_dataset_metadata_from_dcat(dataset_id=None, category=None, name=None):
    """Get metadata for a dataset from DCAT catalog"""
    logger.info(f"Fetching DCAT metadata from {DCAT_URL}")
    try:
        response = requests.get(DCAT_URL, timeout=60)
        response.raise_for_status()
        catalog = response.json()
        
        if not catalog.get('dataset'):
            logger.warning("No datasets found in DCAT catalog")
            return None
            
        # Filter public datasets
        public_datasets = [d for d in catalog.get("dataset", []) if d.get("accessLevel") == "public"]
        logger.info(f"Found {len(public_datasets)} public datasets in catalog")
        
        if dataset_id:
            # Find by ID
            for dataset in public_datasets:
                if dataset.get("identifier") == dataset_id:
                    return dataset
                    
        elif category and name:
            # Find by category and name
            for dataset in public_datasets:
                for dist in dataset.get("distribution", []):
                    if dist.get("format") == "Esri REST" and "FeatureServer" in dist.get("accessURL", ""):
                        dataset_info = get_dataset_info_from_url(dist.get("accessURL"))
                        if (dataset_info['category'].lower() == category.lower() and 
                            dataset_info['name'].lower() == name.lower()):
                            return dataset
                            
        logger.warning(f"No matching dataset found in DCAT catalog")
        return None
        
    except requests.RequestException as e:
        logger.error(f"Error fetching DCAT metadata: {e}")
        return None
    except Exception as e:
        logger.error(f"Unexpected error parsing DCAT metadata: {e}")
        return None

def generate_mbtiles(geojson_path, mbtiles_path, title, description=None, attribution=None):
    """Generate mbtiles from GeoJSON using Tippecanoe"""
    try:
        # Check if tippecanoe is installed
        try:
            subprocess.run(["tippecanoe", "--version"], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except (subprocess.SubprocessError, FileNotFoundError):
            logger.error("Tippecanoe not found. Please install tippecanoe.")
            return False
            
        # Build the tippecanoe command with a minimal set of reliable parameters
        cmd = [
            "tippecanoe",
            "-o", mbtiles_path,
            "-z17",                   # Set maximum zoom level to 17
            "-Z0",                    # Start from zoom level 0
            "-f",                     # Force overwrite
            "-r1",                    # Simplification level 1 (minimal simplification)
            "-pk",                    # Don't simplify the geometries at maxzoom
            "-pf",                    # Don't drop features at lower zoom levels
            "--no-feature-limit",     # Don't limit number of features
            "--detect-shared-borders",  # Better polygon rendering
            "--read-parallel",        # Speed up processing
            "--generate-ids",         # Generate feature IDs
            "--force",
            "--name", title,          # Dataset name
        ]
        
        # Add description (truncate if too long)
        if description:
            # Truncate long descriptions
            if len(description) > 1000:
                truncated_description = description[:997] + "..."
                cmd.extend(["--description", truncated_description])
            else:
                cmd.extend(["--description", description])
        
        if attribution and len(attribution) < 255:  # Keep attribution reasonable
            cmd.extend(["--attribution", attribution])
        
        # Add the input file
        cmd.append(geojson_path)
        
        logger.info(f"Running command: {' '.join(cmd)}")
        
        # Run the command and capture output
        process = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        stdout, stderr = process.communicate()
        
        if process.returncode != 0:
            logger.error(f"Tippecanoe error: {stderr.decode('utf-8')}")
            return False
            
        logger.info(f"Successfully generated {mbtiles_path}")
        return True
    except subprocess.CalledProcessError as e:
        logger.error(f"Error during tile generation: {e}")
        return False
    except Exception as e:
        logger.error(f"Unexpected error during tile generation: {e}")
        return False

def process_single_tileset():
    """Process a single tileset using the provided environment variables"""
    # Validate inputs
    if not FEATURE_SERVICE_URL:
        logger.error("FEATURE_SERVICE_URL is required")
        sys.exit(1)
    
    if not CATEGORY:
        logger.error("CATEGORY is required")
        sys.exit(1)
    
    if not DATASET_NAME:
        logger.error("DATASET_NAME is required")
        sys.exit(1)
    
    # Create working directories
    os.makedirs(WORKING_DIR, exist_ok=True)
    geojson_dir = os.path.join(WORKING_DIR, "geojson", CATEGORY)
    mbtiles_dir = os.path.join(WORKING_DIR, "mbtiles", CATEGORY)
    os.makedirs(geojson_dir, exist_ok=True)
    os.makedirs(mbtiles_dir, exist_ok=True)
    
    # Final destination for the mbtiles file
    output_category_dir = os.path.join(OUTPUT_DIR, CATEGORY)
    os.makedirs(output_category_dir, exist_ok=True)
    
    # Construct file paths
    geojson_path = os.path.join(geojson_dir, f"{DATASET_NAME}.geojson")
    temp_mbtiles_path = os.path.join(mbtiles_dir, f"{DATASET_NAME}.mbtiles")
    final_mbtiles_path = os.path.join(output_category_dir, f"{DATASET_NAME}.mbtiles")
    
    # Try to get metadata from DCAT
    try:
        dataset_metadata = get_dataset_metadata_from_dcat(category=CATEGORY, name=DATASET_NAME)
        
        # If we have metadata, use it for description and attribution
        if dataset_metadata:
            logger.info("Found metadata in DCAT catalog")
            
            global DESCRIPTION, ATTRIBUTION
            
            if not DESCRIPTION and dataset_metadata.get("description"):
                DESCRIPTION = dataset_metadata.get("description")
                
            if not ATTRIBUTION and dataset_metadata.get("agency"):
                ATTRIBUTION = dataset_metadata.get("agency")
    except Exception as e:
        logger.warning(f"Error getting metadata from DCAT: {e}")
    
    # Download data from Feature Service
    logger.info(f"Downloading data from {FEATURE_SERVICE_URL} to {geojson_path}")
    if not download_from_feature_service(FEATURE_SERVICE_URL, geojson_path):
        logger.error("Failed to download data")
        sys.exit(1)
    
    # Check if we got valid GeoJSON
    try:
        with open(geojson_path, 'r') as f:
            geojson_data = json.load(f)
            if 'features' not in geojson_data:
                logger.error("Downloaded file is not valid GeoJSON - missing 'features' property")
                sys.exit(1)
            
            feature_count = len(geojson_data['features'])
            if feature_count == 0:
                logger.error("No features found in downloaded GeoJSON")
                sys.exit(1)
            
            logger.info(f"Successfully downloaded GeoJSON with {feature_count} features")
    except json.JSONDecodeError:
        logger.error("Downloaded file is not valid JSON")
        sys.exit(1)
    except Exception as e:
        logger.error(f"Error validating GeoJSON: {e}")
        sys.exit(1)
    
    # Generate mbtiles with Tippecanoe
    logger.info(f"Generating tiles with tippecanoe to {temp_mbtiles_path}")
    if not generate_mbtiles(geojson_path, temp_mbtiles_path, TITLE, DESCRIPTION, ATTRIBUTION):
        logger.error("Failed to generate mbtiles")
        sys.exit(1)
    
    # Move the mbtiles file to the final destination
    logger.info(f"Moving mbtiles file to {final_mbtiles_path}")
    try:
        # First remove the existing file if it exists
        if os.path.exists(final_mbtiles_path):
            os.remove(final_mbtiles_path)
        
        # Copy the new file
        with open(temp_mbtiles_path, 'rb') as src, open(final_mbtiles_path, 'wb') as dst:
            dst.write(src.read())
        
        logger.info(f"Successfully moved mbtiles file to {final_mbtiles_path}")
        
        # Add metadata.json file to record DCAT information
        metadata_dir = os.path.join(OUTPUT_DIR, "metadata")
        os.makedirs(metadata_dir, exist_ok=True)
        
        metadata_file = os.path.join(metadata_dir, f"{CATEGORY}_{DATASET_NAME}.json")
        metadata = {
            "category": CATEGORY,
            "name": DATASET_NAME,
            "title": TITLE,
            "description": DESCRIPTION,
            "attribution": ATTRIBUTION,
            "feature_service_url": FEATURE_SERVICE_URL,
            "timestamp": time.time(),
            "tile_url": f"/services/{CATEGORY}/{DATASET_NAME}/tiles/{{z}}/{{x}}/{{y}}.pbf"
        }
        
        with open(metadata_file, 'w') as f:
            json.dump(metadata, f, indent=2)
        
        # Success!
        print(f"Successfully generated mbtiles: {CATEGORY}/{DATASET_NAME}")
        sys.exit(0)
    except Exception as e:
        logger.error(f"Error moving mbtiles file: {e}")
        sys.exit(1)

def process_all_datasets():
    """Process all public datasets from DCAT catalog"""
    datasets_processed = []
    
    # Create working directories
    geojson_dir = os.path.join(WORKING_DIR, "geojson")
    mbtiles_dir = os.path.join(WORKING_DIR, "mbtiles")
    os.makedirs(geojson_dir, exist_ok=True)
    os.makedirs(mbtiles_dir, exist_ok=True)
    
    # Create metadata directory
    metadata_dir = os.path.join(OUTPUT_DIR, "metadata")
    os.makedirs(metadata_dir, exist_ok=True)
    
    # Fetch DCAT metadata
    logger.info(f"Fetching DCAT metadata from {DCAT_URL}")
    try:
        response = requests.get(DCAT_URL)
        response.raise_for_status()
        catalog = response.json()
        logger.info(f"Found {len(catalog.get('dataset', []))} datasets in catalog")
    except Exception as e:
        logger.error(f"Error fetching DCAT metadata: {e}")
        sys.exit(1)
    
    # Filter to just public datasets
    public_datasets = [d for d in catalog.get("dataset", []) if d.get("accessLevel") == "public"]
    logger.info(f"Found {len(public_datasets)} public datasets")
    
    # Process each dataset
    for dataset in public_datasets:
        dataset_id = dataset.get("identifier")
        title = dataset.get("title", "Untitled")
        logger.info(f"Processing dataset: {title} ({dataset_id})")
        
        # Initialize dataset info
        dataset_info = {
            'category': "uncategorized",
            'name': None
        }
        
        # Get feature service URL
        feature_service_url = None
        for dist in dataset.get("distribution", []):
            if dist.get("format") == "Esri REST" and "FeatureServer" in dist.get("accessURL", ""):
                feature_service_url = dist.get("accessURL")
                dataset_info = get_dataset_info_from_url(feature_service_url)
                logger.info(f"Found Feature Service URL: {feature_service_url}")
                break
        
        if not feature_service_url:
            logger.info(f"Skipping {title} - no Feature Service URL")
            continue
        
        # If no name was found, use a sanitized version of the title
        if not dataset_info['name']:
            dataset_info['name'] = title.replace(" ", "_").lower()
        
        category = dataset_info['category']
        dataset_name = dataset_info['name']
        
        logger.info(f"Processing {category}/{dataset_name}")

        # Create category directories locally
        geojson_category_dir = os.path.join(geojson_dir, category)
        mbtiles_category_dir = os.path.join(mbtiles_dir, category)
        
        os.makedirs(geojson_category_dir, exist_ok=True)
        os.makedirs(mbtiles_category_dir, exist_ok=True)
        
        # Create category directory in output
        output_category_dir = os.path.join(OUTPUT_DIR, category)
        os.makedirs(output_category_dir, exist_ok=True)
            
        # Download data from Feature Service
        geojson_path = os.path.join(geojson_category_dir, f"{dataset_name}.geojson")
        logger.info(f"Downloading data to {geojson_path}")
        
        # First try the direct GeoJSON URL
        geojson_url = None
        for dist in dataset.get("distribution", []):
            if dist.get("format") == "GeoJSON" and dist.get("downloadURL"):
                geojson_url = dist.get("downloadURL")
                logger.info(f"Found GeoJSON URL: {geojson_url}")
                break
        
        download_success = False
        if geojson_url:
            logger.info(f"Trying direct GeoJSON download first...")
            try:
                response = requests.get(geojson_url, timeout=120)
                if response.ok and response.text and not '"status":"Failed' in response.text:
                    with open(geojson_path, 'wb') as f:
                        f.write(response.content)
                    download_success = True
                    logger.info("Direct download successful")
                else:
                    logger.warning(f"Direct download failed or returned error content")
            except Exception as e:
                logger.error(f"Error during direct download: {e}")
        
        # If direct download failed, use chunked download
        if not download_success:
            logger.info("Using chunked download from Feature Service...")
            download_success = download_from_feature_service(feature_service_url, geojson_path)
        
        if not download_success:
            logger.error(f"Failed to download data for {dataset_name}, skipping")
            continue
        
        # Check if we got valid GeoJSON
        try:
            with open(geojson_path, 'r') as f:
                test_data = json.load(f)
                if 'features' not in test_data:
                    logger.error(f"Downloaded file is not valid GeoJSON")
                    continue
                else:
                    logger.info(f"Valid GeoJSON with {len(test_data.get('features', []))} features")
        except json.JSONDecodeError as e:
            logger.error(f"Downloaded file is not valid JSON: {e}")
            continue
        
        # Extract basic attributes
        attribution = dataset.get("agency", "")
        description = dataset.get("description", "")
        
        # Generate tiles with Tippecanoe
        mbtiles_path = os.path.join(mbtiles_category_dir, f"{dataset_name}.mbtiles")
        logger.info(f"Generating tiles with tippecanoe to {mbtiles_path}")
        
        # Generate mbtiles
        if generate_mbtiles(geojson_path, mbtiles_path, title, description, attribution):
            # Copy to final destination
            final_mbtiles_path = os.path.join(output_category_dir, f"{dataset_name}.mbtiles")
            
            try:
                # First remove the existing file if it exists
                if os.path.exists(final_mbtiles_path):
                    os.remove(final_mbtiles_path)
                
                # Copy the new file
                with open(mbtiles_path, 'rb') as src, open(final_mbtiles_path, 'wb') as dst:
                    dst.write(src.read())
                
                logger.info(f"Successfully moved mbtiles file to {final_mbtiles_path}")
                
                # Add to processed datasets
                datasets_processed.append({
                    "id": dataset_id,
                    "title": title,
                    "category": category,
                    "name": dataset_name,
                    "feature_service_url": feature_service_url,
                    "description": description,
                    "attribution": attribution,
                    "timestamp": time.time(),
                    "tile_url": f"/services/{category}/{dataset_name}/tiles/{{z}}/{{x}}/{{y}}.pbf"
                })
                
                # Write individual metadata
                metadata_file = os.path.join(metadata_dir, f"{category}_{dataset_name}.json")
                with open(metadata_file, 'w') as f:
                    json.dump(datasets_processed[-1], f, indent=2)
                
                logger.info(f"Successfully processed dataset {dataset_name}")
            except Exception as e:
                logger.error(f"Error moving mbtiles file for {dataset_name}: {e}")
        else:
            logger.error(f"Failed to generate mbtiles for {dataset_name}")
    
    # Write combined metadata file
    combined_metadata_path = os.path.join(OUTPUT_DIR, "metadata.json")
    with open(combined_metadata_path, 'w') as f:
        json.dump(datasets_processed, f, indent=2)
    
    logger.info(f"Processed {len(datasets_processed)} datasets")
    print(f"Successfully processed {len(datasets_processed)} datasets")
    return datasets_processed

def main():
    """Main function to determine processing mode and execute"""
    # Decide which mode to run in
    if REFRESH_MODE.lower() == "all":
        logger.info("Running in bulk processing mode")
        process_all_datasets()
    else:
        logger.info("Running in single tileset mode")
        process_single_tileset()

if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        logger.error(f"Fatal error in main process: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
`
	
	return os.WriteFile(scriptPath, []byte(scriptContent), 0755)
}

// writeJSONResponse is a helper function to write JSON responses
func writeJSONResponse(w http.ResponseWriter, statusCode int, response interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Errorf("Error encoding JSON response: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
	}}