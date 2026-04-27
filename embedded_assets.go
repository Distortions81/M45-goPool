package main

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed data/templates data/www
var embeddedAssets embed.FS

const (
	embeddedTemplatesRoot = "data/templates"
	embeddedWWWRoot       = "data/www"
)

type uiAssetLoader struct {
	templates fs.FS
	static    fs.FS
}

func newUIAssetLoader() (*uiAssetLoader, error) {
	return newEmbeddedUIAssetLoader()
}

func newEmbeddedUIAssetLoader() (*uiAssetLoader, error) {
	templates, err := embeddedAssetFS(embeddedTemplatesRoot)
	if err != nil {
		return nil, fmt.Errorf("open embedded templates: %w", err)
	}
	static, err := embeddedAssetFS(embeddedWWWRoot)
	if err != nil {
		return nil, fmt.Errorf("open embedded static assets: %w", err)
	}
	return &uiAssetLoader{
		templates: templates,
		static:    static,
	}, nil
}

func embeddedAssetFS(root string) (fs.FS, error) {
	return fs.Sub(embeddedAssets, root)
}

func (l *uiAssetLoader) readTemplate(name string) ([]byte, error) {
	if l == nil || l.templates == nil {
		return nil, fmt.Errorf("template asset filesystem not configured")
	}
	return fs.ReadFile(l.templates, name)
}

func (l *uiAssetLoader) staticFiles() (fs.FS, error) {
	if l == nil || l.static == nil {
		return nil, fmt.Errorf("static asset filesystem not configured")
	}
	return l.static, nil
}
