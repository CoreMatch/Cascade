package game

import (
	"fmt"
	"path/filepath"

	"github.com/lafriks/go-tiled"
)

// Map represents a game map with collision information.
type Map struct {
	Name       string
	Width      int
	Height     int
	TileWidth  int
	TileHeight int
	// CollisionGrid stores whether a tile at (x, y) is an obstacle.
	// For now, we use a simple boolean grid.
	// 0: walkable, 1: blocked (land), 2: water
	CollisionGrid [][]byte
}

const (
	TerrainWalkable = 0
	TerrainBlocked  = 1
	TerrainWater    = 2
)

// LoadMap loads a TMX map from the given path.
func LoadMap(path string) (*Map, error) {
	tiledMap, err := tiled.LoadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load tmx file: %w", err)
	}

	m := &Map{
		Name:       filepath.Base(path),
		Width:      tiledMap.Width,
		Height:     tiledMap.Height,
		TileWidth:  tiledMap.TileWidth,
		TileHeight: tiledMap.TileHeight,
	}

	// Initialize collision grid
	m.CollisionGrid = make([][]byte, m.Height)
	for i := range m.CollisionGrid {
		m.CollisionGrid[i] = make([]byte, m.Width)
	}

	// Simple logic: iterate through all layers and tiles.
	// If any tile has "blocked", "large-rock", "tree" properties, mark as blocked.
	// If it has "water", mark as water.
	for _, layer := range tiledMap.Layers {
		for y := 0; y < m.Height; y++ {
			for x := 0; x < m.Width; x++ {
				tile := layer.Tiles[y*m.Width+x]
				if tile.Nil {
					continue
				}

				// Check tile properties
				var props tiled.Properties
				for _, t := range tile.Tileset.Tiles {
					if t.ID == tile.ID {
						props = t.Properties
						break
					}
				}

				for _, prop := range props {
					switch prop.Name {
					case "blocked", "large-rock", "tree", "mountain":
						m.CollisionGrid[y][x] = TerrainBlocked
					case "water", "deepwater":
						// Only set to water if it wasn't already blocked by something else
						if m.CollisionGrid[y][x] == TerrainWalkable {
							m.CollisionGrid[y][x] = TerrainWater
						}
					}
				}
			}
		}
	}

	return m, nil
}

// IsWalkable checks if a world position (x, y) is walkable for a unit.
// For now, we assume land units.
func (m *Map) IsWalkable(x, y float32) bool {
	tileX := int(x / float32(m.TileWidth))
	tileY := int(y / float32(m.TileHeight))

	if tileX < 0 || tileX >= m.Width || tileY < 0 || tileY >= m.Height {
		return false
	}

	return m.CollisionGrid[tileY][tileX] == TerrainWalkable
}
