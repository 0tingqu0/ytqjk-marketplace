# Third-party notices

## Caveman skill

YTQJK bundles an adapted Codex-compatible copy of Matt Pocock's historical
`caveman` skill. Audited upstream snapshot:
<https://github.com/mattpocock/skills/tree/221ffca/skills/productivity/caveman>
The behavior text is preserved; frontmatter and attribution were adapted for
this plugin. Upstream removed `caveman` after that revision, so YTQJK carries
the audited snapshot instead of depending on a moving install target.

MIT License

Copyright (c) 2026 Matt Pocock

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Go SQLite runtime

YTQJK uses `modernc.org/sqlite` and its declared Go module dependencies to
provide a pure-Go SQLite driver. The driver source is distributed under its
upstream three-clause BSD-style license. Embedded SQLite is dedicated to the
public domain. The module also carries its upstream notices for optional
components. YTQJK does not relicense those components; the exact dependency
versions are recorded in `go.mod` and `go.sum`.
