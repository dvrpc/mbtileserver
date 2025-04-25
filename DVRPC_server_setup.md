# DVRPC MBTileServer Setup Documentation

## System Requirements
- Ubuntu Server (tested on Ubuntu 22.04 LTS)
- Minimum 4GB RAM, 2 CPU cores recommended for processing large datasets
- Sufficient disk space for tile storage

## Installation Steps

### 1. Update System Packages
```bash
sudo apt update && sudo apt upgrade -y
```

### 2. Install Required Dependencies
```bash
# Install GDAL for ogr2ogr
sudo apt-get install gdal-bin

# Install Go programming language
sudo apt install golang-go --fix-missing

# Install Python virtual environment
sudo apt install python3-venv -y

# Install build tools for Tippecanoe
sudo apt install make
sudo apt install build-essential
sudo apt-get install gcc g++ make libsqlite3-dev zlib1g-dev
```

### 3. Install Tippecanoe (Vector Tile Generator)
```bash
git clone https://github.com/mapbox/tippecanoe.git
cd tippecanoe
sudo make install
cd
```

### 4. Clone and Set Up MBTileServer
```bash
# Clone the develop branch of the repository
git clone -b develop https://github.com/dvrpc/mbtileserver.git
```

### 5. Set Up Python Environment for Tilemaker
```bash
# Create tilemaker directory if it doesn't exist
mkdir -p mbtileserver/tilemaker

# Set up Python virtual environment in the tilemaker directory
cd mbtileserver/tilemaker
python3 -m venv venv

# Activate the virtual environment
source ./venv/bin/activate

# Install Python dependencies 
# (If there's a requirements.txt file)
pip install -r requirements.txt

# Or install the specific packages needed
pip install psycopg2-binary requests

# Deactivate when done (optional)
# deactivate
```

### 6. Build MBTileServer
```bash
# Navigate to the mbtileserver directory
cd ~/mbtileserver

# Build the Go application
go build
```

### 7. Create Systemd Service
Create a service file to run MBTileServer as a background service:

```bash
sudo nano /etc/systemd/system/mbtileserver.service
```

Add the following content to the service file:

```ini
[Unit]
Description=MBTileServer - Tile Map Server
After=network.target

[Service]
Type=simple
User=dvrpcgis
WorkingDirectory=/home/dvrpcgis/mbtileserver
ExecStart=/home/dvrpcgis/mbtileserver/mbtileserver --dir ./tilesets --enable-refresh --enable-fs-watch --basemap-tiles-url "https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}.png" --root-url /data
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

### 8. Enable and Start the Service
```bash
# Set proper permissions
sudo chmod 644 /etc/systemd/system/mbtileserver.service

# Reload systemd configuration
sudo systemctl daemon-reload

# Enable service to start on boot
sudo systemctl enable mbtileserver.service

# Start the service
sudo systemctl start mbtileserver.service
```

### 9. Verify Installation
```bash
# Check service status
sudo systemctl status mbtileserver.service

# Check if the server is responding
curl http://localhost:8000/
```

## Configuration Options

The MBTileServer is configured with the following options:
- `--dir ./tilesets`: Directory for storing tile files
- `--enable-refresh`: Enables tile refresh endpoint
- `--enable-fs-watch`: Watch for file system changes
- `--basemap-tiles-url`: URL for background map tiles
- `--root-url /data`: Base URL path for the service

## Maintenance 

### Viewing Logs
```bash
sudo journalctl -u mbtileserver.service -f
```

### Restarting the Service
```bash
sudo systemctl restart mbtileserver.service
```

### Building After Code Changes
```bash
cd ~/mbtileserver
go build
sudo systemctl restart mbtileserver.service
```

### Working with the Python Environment
```bash
# Activate the virtual environment when needed
cd ~/mbtileserver/tilemaker
source ./venv/bin/activate

# Run Python scripts within the environment
python tilemaker.py

# Install additional Python packages if needed
pip install package-name

# Deactivate the environment when done
deactivate

# Create .env with read-only credentials to DVRPC GIS db
```