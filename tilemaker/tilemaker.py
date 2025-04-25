"""
Streamlined script to generate mbtiles directly from PostgreSQL database with DCAT metadata.
This bypasses ArcGIS REST API completely and only uses direct database connections.
Includes proper coordinate transformation from source projection to WGS84.
"""

import json
import os
import subprocess
import requests
import time
import logging
import sys
import sqlite3
import psycopg2
from psycopg2 import sql
from pathlib import Path
from dotenv import load_dotenv
import tempfile
import shutil

load_dotenv()

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger('tilemaker')

# Configuration
DCAT_URL = os.environ.get("DCAT_URL", "https://arcgis.dvrpc.org/api/dcat.json")

# Database connection parameters
PG_HOST = os.environ.get("PG_HOST", "dvrpcgis-db.postgres.database.azure.com")
PG_PORT = os.environ.get("PG_PORT", "5432")
PG_DATABASE = os.environ.get("PG_DATABASE", "gis")
PG_USER = os.environ.get("PG_USER", "")
PG_PASSWORD = os.environ.get("PG_PASSWORD", "")
PG_SSL_MODE = os.environ.get("PG_SSL_MODE", "require")  # Azure PostgreSQL requires SSL

# Get configuration from environment variables
CATEGORY = os.environ.get("CATEGORY")
DATASET_NAME = os.environ.get("DATASET_NAME")
TITLE = os.environ.get("TITLE", DATASET_NAME)
DESCRIPTION = os.environ.get("DESCRIPTION", "")
ATTRIBUTION = os.environ.get("ATTRIBUTION", "")
OUTPUT_DIR = os.environ.get("TILE_DIR", "./tilesets")
WORKING_DIR = os.environ.get("WORKING_DIR", "/tmp/tilemaker")

def get_database_connection():
    """Establish a connection to the PostgreSQL database"""
    try:
        conn = psycopg2.connect(
            host=PG_HOST,
            port=PG_PORT,
            dbname=PG_DATABASE,
            user=PG_USER,
            password=PG_PASSWORD,
            sslmode=PG_SSL_MODE
        )
        logger.info(f"Successfully connected to PostgreSQL database at {PG_HOST}")
        return conn
    except Exception as e:
        logger.error(f"Error connecting to PostgreSQL database: {e}")
        return None

def check_table_exists(conn, schema, table):
    """Check if the specified table exists in the database"""
    try:
        cursor = conn.cursor()
        cursor.execute(sql.SQL("""
            SELECT EXISTS (
                SELECT 1
                FROM information_schema.tables
                WHERE table_schema = %s
                AND table_name = %s
            )
        """), (schema, table))
        
        exists = cursor.fetchone()[0]
        cursor.close()
        return exists
    except Exception as e:
        logger.error(f"Error checking if table exists: {e}")
        return False

def get_geometry_srid(conn, schema, table):
    """Get the SRID of the geometry column in the table"""
    try:
        cursor = conn.cursor()
        cursor.execute(sql.SQL("""
            SELECT srid 
            FROM geometry_columns 
            WHERE f_table_schema = %s 
            AND f_table_name = %s
        """), (schema, table))
        
        result = cursor.fetchone()
        cursor.close()
        
        if result:
            return result[0]
        return None
    except Exception as e:
        logger.error(f"Error getting geometry SRID: {e}")
        return None

def export_table_to_geojson(conn, schema, table, output_file):
    """Export a PostgreSQL table to GeoJSON using ogr2ogr with proper transformation"""
    try:
        # Get the source SRID
        source_srid = get_geometry_srid(conn, schema, table)
        if not source_srid:
            logger.warning(f"Could not determine SRID for {schema}.{table}, assuming 4326")
            source_srid = 4326
            
        logger.info(f"Source geometry SRID: {source_srid}")
        
        # Construct the PostgreSQL connection string for ogr2ogr
        pg_conn_str = f"PG:host={PG_HOST} port={PG_PORT} dbname={PG_DATABASE} user={PG_USER} password={PG_PASSWORD} sslmode={PG_SSL_MODE}"
        
        # Construct the table specification
        table_spec = f"{schema}.{table}"
        
        # Build ogr2ogr command with transformation
        cmd = [
            "ogr2ogr",
            "-f", "GeoJSON",
            "-t_srs", "EPSG:4326",  # Target SRS (WGS84)
            output_file,
            pg_conn_str,
            table_spec
        ]
        
        # If source is not already 4326, add explicit source SRS
        if source_srid != 4326:
            cmd.extend(["-s_srs", f"EPSG:{source_srid}"])
        
        logger.info(f"Running command: {' '.join(cmd)}")
        
        process = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        stdout, stderr = process.communicate()
        
        if process.returncode != 0:
            logger.error(f"ogr2ogr error: {stderr.decode('utf-8')}")
            return False
            
        logger.info(f"Successfully exported table {table_spec} to {output_file}")
        
        # Verify the file exists and has content
        if not os.path.exists(output_file) or os.path.getsize(output_file) == 0:
            logger.error(f"Export file {output_file} is empty or does not exist")
            return False
            
        # Verify it's valid GeoJSON
        try:
            with open(output_file, 'r') as f:
                geojson_data = json.load(f)
                
            if 'features' not in geojson_data:
                logger.error("Exported file is not valid GeoJSON - missing 'features' property")
                return False
                
            feature_count = len(geojson_data['features'])
            logger.info(f"Exported GeoJSON contains {feature_count} features")
            
            if feature_count == 0:
                logger.warning("No features found in exported GeoJSON")
                return False
            
            # Verify a sample coordinate to ensure transformation worked
            if feature_count > 0 and 'geometry' in geojson_data['features'][0]:
                geom = geojson_data['features'][0]['geometry']
                if geom['type'] == 'Point':
                    coords = geom['coordinates']
                    logger.info(f"Sample coordinates (longitude, latitude): {coords}")
                    # Check if coordinates look like WGS84 (rough check for Philadelphia area)
                    if (-180 <= coords[0] <= 180) and (-90 <= coords[1] <= 90):
                        logger.info("Coordinate transformation appears successful")
                    else:
                        logger.warning("Coordinates might not be properly transformed to WGS84")
                
            return True
        except Exception as e:
            logger.error(f"Error validating GeoJSON: {e}")
            return False
    except Exception as e:
        logger.error(f"Error exporting table to GeoJSON: {e}")
        return False

def get_dataset_metadata_from_dcat(category=None, name=None):
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
        
        # First try to match by category and name
        if category and name:
            for dataset in public_datasets:
                for dist in dataset.get("distribution", []):
                    if dist.get("format") == "Esri REST" and "FeatureServer" in dist.get("accessURL", ""):
                        url_parts = dist.get("accessURL", "").split('/')
                        
                        # Find "services" in the path
                        for i, part in enumerate(url_parts):
                            if part == "services" and i+2 < len(url_parts):
                                url_category = url_parts[i+1]
                                url_name = url_parts[i+2]
                                
                                if (url_category.lower() == category.lower() and 
                                    url_name.lower() == name.lower()):
                                    return dataset
        
        # If no match found, just return None
        logger.warning(f"No matching dataset found in DCAT catalog")
        return None
        
    except Exception as e:
        logger.error(f"Error getting DCAT metadata: {e}")
        return None

def add_custom_metadata_to_mbtiles(mbtiles_path, metadata):
    """Add custom metadata directly to an MBTiles file's metadata table"""
    try:
        conn = sqlite3.connect(mbtiles_path)
        cursor = conn.cursor()
        
        # Add each metadata key-value pair
        for key, value in metadata.items():
            # Make sure value is a string
            if not isinstance(value, str):
                value = json.dumps(value)
            
            # Insert or replace metadata entry
            cursor.execute(
                "INSERT OR REPLACE INTO metadata (name, value) VALUES (?, ?)",
                (key, value)
            )
        
        conn.commit()
        conn.close()
        logger.info(f"Successfully added custom metadata to {mbtiles_path}")
        return True
    except Exception as e:
        logger.error(f"Error adding metadata to MBTiles: {e}")
        return False

def generate_mbtiles(geojson_path, mbtiles_path, title, description=None, attribution=None, category=None, dataset_name=None):
    """Generate mbtiles from GeoJSON using Tippecanoe and add custom metadata"""
    try:
        # Check if tippecanoe is installed
        try:
            subprocess.run(["tippecanoe", "--version"], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except (subprocess.SubprocessError, FileNotFoundError):
            logger.error("Tippecanoe not found. Please install tippecanoe.")
            return False
        
        # Build the tippecanoe command with memory-efficient parameters
        cmd = [
            "tippecanoe",
            "-o", mbtiles_path,
            "-z16",                   # Set maximum zoom level to 16
            "-Z0",                    # Start from zoom level 0
            "-f",                     # Force overwrite
            "-r1",                    # Simplification level 1 (minimal simplification)
            "-pk",                    # Don't simplify the geometries at maxzoom
            "-pf",                    # Don't drop features at lower zoom levels
            "--no-feature-limit",     # Don't limit number of features
            "--drop-densest-as-needed",  # Drop points at each zoom level as needed (memory efficient)
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
        
        # Add custom metadata directly to the MBTiles file
        if category and dataset_name:
            # Prepare custom metadata
            custom_metadata = {
                "category": category,
                "dataset_name": dataset_name,
                "tile_url": f"/data/{category}/{dataset_name}/tiles/{{z}}/{{x}}/{{y}}.pbf",
                "scheme": "xyz",
                "tilejson": "2.1.0",
                "tilesize": "512",
                "format": "pbf",
                "type": "overlay",
                "timestamp": str(time.time()),
            }
            
            # Add custom metadata to MBTiles file
            add_custom_metadata_to_mbtiles(mbtiles_path, custom_metadata)
            
        return True
    except Exception as e:
        logger.error(f"Error during tile generation: {e}")
        return False

def process_single_tileset():
    """Process a single tileset by connecting directly to the database"""
    # Validate inputs
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
        
        # If we have metadata, use it for description, attribution, and title
        if dataset_metadata:
            logger.info("Found metadata in DCAT catalog")
            
            global DESCRIPTION, ATTRIBUTION, TITLE
            
            # Use the title from DCAT metadata
            if dataset_metadata.get("title"):
                TITLE = dataset_metadata.get("title")
                logger.info(f"Using title from DCAT metadata: {TITLE}")
            
            if not DESCRIPTION and dataset_metadata.get("description"):
                DESCRIPTION = dataset_metadata.get("description")
                
            if not ATTRIBUTION and dataset_metadata.get("agency"):
                ATTRIBUTION = dataset_metadata.get("agency")
    except Exception as e:
        logger.warning(f"Error getting metadata from DCAT: {e}")
    
    # Connect to the database
    conn = get_database_connection()
    if not conn:
        logger.error("Failed to connect to the database")
        sys.exit(1)
    
    # Check if the table exists
    if not check_table_exists(conn, CATEGORY, DATASET_NAME):
        logger.error(f"Table {CATEGORY}.{DATASET_NAME} does not exist in the database")
        conn.close()
        sys.exit(1)
    
    # Export the table to GeoJSON with proper coordinate transformation
    logger.info(f"Exporting table {CATEGORY}.{DATASET_NAME} to GeoJSON with coordinate transformation")
    if not export_table_to_geojson(conn, CATEGORY, DATASET_NAME, geojson_path):
        logger.error("Failed to export table to GeoJSON")
        conn.close()
        sys.exit(1)
    
    # Close the database connection
    conn.close()
    
    # Generate mbtiles with Tippecanoe
    logger.info(f"Generating tiles with tippecanoe to {temp_mbtiles_path}")
    if not generate_mbtiles(geojson_path, temp_mbtiles_path, TITLE, DESCRIPTION, ATTRIBUTION, CATEGORY, DATASET_NAME):
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
        
        # Success!
        print(f"Successfully generated mbtiles: {CATEGORY}/{DATASET_NAME}")
        sys.exit(0)
    except Exception as e:
        logger.error(f"Error moving mbtiles file: {e}")
        sys.exit(1)


def main():
    """Main function to determine processing mode and execute"""
    # Check if ogr2ogr is available
    try:
        subprocess.run(["ogr2ogr", "--version"], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except (subprocess.SubprocessError, FileNotFoundError):
        logger.error("ogr2ogr not found. Please install GDAL/OGR tools.")
        sys.exit(1)
    
    # Check if tippecanoe is available
    try:
        subprocess.run(["tippecanoe", "--version"], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except (subprocess.SubprocessError, FileNotFoundError):
        logger.error("tippecanoe not found. Please install tippecanoe.")
        sys.exit(1)
    
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