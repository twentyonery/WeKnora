#!/usr/bin/env python3
"""
Build a .pptx deck from a JSON outline.

Usage:
    python make_pptx.py <outline.json> <output.pptx> [--title T] [--author A]

Requires python-pptx (the skill venv ships with it; otherwise
`pip install python-pptx`).

Outline JSON is a list of slides; each slide is a dict with a "layout" key:

    {"layout": "title", "title": str, "subtitle": str}
    {"layout": "bullets", "title": str,
     "bullets": [str | {"text": str, "level": 0-4}, ...]}
    {"layout": "two_column", "title": str,
     "left": [...], "right": [...]}          # same bullet item shape as above
"""

import argparse
import json
import sys

try:
    from pptx import Presentation
    from pptx.util import Pt
except ImportError:  # pragma: no cover
    sys.exit("python-pptx is required: pip install python-pptx")


def _add_bullets(text_frame, items):
    """Append bullet items to a content placeholder's text frame."""
    first = True
    for item in items or []:
        if isinstance(item, dict):
            text = str(item.get("text", ""))
            level = int(item.get("level", 0))
        else:
            text, level = str(item), 0
        level = max(0, min(4, level))
        if first:
            paragraph = text_frame.paragraphs[0]
            first = False
        else:
            paragraph = text_frame.add_paragraph()
        paragraph.text = text
        paragraph.level = level


def build_deck(outline, title, author):
    prs = Presentation()
    made_title_slide = False

    for slide_spec in outline:
        layout = slide_spec.get("layout", "bullets")

        if layout == "title" and not made_title_slide:
            slide = prs.slides.add_slide(prs.slide_layouts[0])
            slide.shapes.title.text = slide_spec.get("title") or title or ""
            subtitle = slide.placeholders[1].text_frame
            subtitle.text = slide_spec.get("subtitle") or author or ""
            made_title_slide = True
            continue

        if layout == "two_column":
            slide = prs.slides.add_slide(prs.slide_layouts[3])
            slide.shapes.title.text = slide_spec.get("title", "")
            _add_bullets(slide.placeholders[1].text_frame, slide_spec.get("left"))
            _add_bullets(slide.placeholders[2].text_frame, slide_spec.get("right"))
            continue

        # "title" slides after the first degrade to section headers.
        if layout == "title":
            slide = prs.slides.add_slide(prs.slide_layouts[5])  # title only
            slide.shapes.title.text = slide_spec.get("title", "")
            continue

        slide = prs.slides.add_slide(prs.slide_layouts[1])  # title + content
        slide.shapes.title.text = slide_spec.get("title", "")
        _add_bullets(slide.placeholders[1].text_frame, slide_spec.get("bullets"))

    if not made_title_slide:
        slide = prs.slides.add_slide(prs.slide_layouts[0])
        slide.shapes.title.text = title or "Presentation"
        slide.placeholders[1].text = author or ""

    return prs


def main():
    parser = argparse.ArgumentParser(description="Build a pptx from a JSON outline")
    parser.add_argument("outline", help="path to outline JSON")
    parser.add_argument("output", help="output .pptx path (use /workspace/output/)")
    parser.add_argument("--title", default="", help="deck title")
    parser.add_argument("--author", default="", help="deck author / subtitle")
    args = parser.parse_args()

    with open(args.outline, encoding="utf-8") as fh:
        outline = json.load(fh)
    if not isinstance(outline, list):
        sys.exit("outline JSON must be a list of slides")

    prs = build_deck(outline, args.title, args.author)
    prs.save(args.output)
    print(f"saved {args.output} ({len(outline)} slides)")


if __name__ == "__main__":
    main()
