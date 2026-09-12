---
name: presentation-gen
description: Generate and edit PowerPoint (.pptx) presentations with python-pptx. Use when the user asks for a slide deck, 演示文稿, PPT, slides, or wants to turn an outline, report, or knowledge-base content into a presentation saved in /workspace/output.
---

# Presentation Generation

Build real `.pptx` files inside the sandbox with `python-pptx`. Always write
the result under `/workspace/output/` so it shows up in the session's
artifact panel, where the user can preview and download it.

## Setup

The sandbox skill installer creates a virtualenv for this skill at
`<skill_dir>/.venv` with `python-pptx` preinstalled. If the venv is
unavailable, install it on the fly:

```bash
pip install python-pptx
```

## Quick Start

```python
from pptx import Presentation
from pptx.util import Inches, Pt

prs = Presentation()

slide = prs.slides.add_slide(prs.slide_layouts[0])  # title slide
slide.shapes.title.text = "Project Review"
slide.placeholders[1].text = "2026 Q3"

bullet = prs.slides.add_slide(prs.slide_layouts[1])  # title + content
bullet.shapes.title.text = "Highlights"
body = bullet.placeholders[1].text_frame
body.text = "First point"
p = body.add_paragraph()
p.text = "Second point"
p.level = 1

prs.save("/workspace/output/deck.pptx")
```

## Utility Script

`scripts/make_pptx.py` turns a JSON outline into a finished deck:

```bash
python scripts/make_pptx.py outline.json /workspace/output/deck.pptx \
    --title "季度回顾" --author "WeKnora"
```

Outline format (a list of slides):

```json
[
  {"layout": "title", "title": "Project Review", "subtitle": "2026 Q3"},
  {"layout": "bullets", "title": "Highlights", "bullets": ["A", "B", {"text": "Nested", "level": 1}]},
  {"layout": "two_column", "title": "Compare", "left": ["x"], "right": ["y"]}
]
```

## Design Guidance

1. One idea per slide; 5-7 bullets maximum, each under two lines.
2. Prefer the built-in layouts (`title`, `bullets`, `two_column`) over
   hand-placed text boxes so the deck stays editable.
3. Set explicit font sizes for dense slides instead of letting text shrink.
4. When the user provides knowledge-base content, summarize it into the
   outline first and show the outline before generating the file.
5. Always save to `/workspace/output/<descriptive-name>.pptx`, then tell the
   user the file name so they can open it from the artifact panel.
