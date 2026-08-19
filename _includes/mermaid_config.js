{
  /* Overrides the theme's default (empty) mermaid config. `startOnLoad` is
     false because components/mermaid.html calls mermaid.run() itself, and the
     dark mermaid theme matches `color_scheme: dark` in _config.yml.

     Block comments only: the theme renders pages through
     _layouts/vendor/compress.html, which strips newlines, so a `//` comment
     would swallow the rest of this script. */
  startOnLoad: false,
    theme: 'dark'
}
