#!/usr/bin/env python3
"""
Normalize Hermes-style headings to odek-standard section names.
Also fix a few remaining description length issues.
"""

import os
import re

ODEK_SKILLS_DIR = os.path.expanduser("~/.odek/skills")

HEADING_MAP = {
    '## When to Use': '## Overview',
    '## Pitfalls': '## Common Pitfalls',
}

def read_file(path):
    with open(path, 'r') as f:
        return f.read()

def write_file(path, content):
    with open(path, 'w') as f:
        f.write(content)

def trim_description(content):
    """Trim description field to 120 chars max."""
    lines = content.split('\n')
    changed = False
    new_lines = []
    in_front = False
    in_desc = False
    desc_line_idx = -1
    
    for i, line in enumerate(lines):
        if line.strip() == '---':
            in_front = not in_front
        if in_front and line.startswith('description:'):
            desc_line_idx = i
        new_lines.append(line)
    
    if desc_line_idx >= 0:
        line = lines[desc_line_idx]
        m = re.match(r'description:\s*(.*)', line)
        if m:
            desc = m.group(1)
            desc = re.sub(r'^["\'](.*)["\']$', r'\1', desc)
            if len(desc) > 120:
                trimmed = desc[:117].rsplit(' ', 1)[0] + '...'
                new_lines[desc_line_idx] = f'description: {trimmed}'
                changed = True
    
    return '\n'.join(new_lines), changed


def normalize_headings(content):
    """Rename Hermes-style headings to odek standard."""
    changed = False
    for old, new in HEADING_MAP.items():
        if old in content:
            content = content.replace(old, new)
            changed = True
    return content, changed


def main():
    skills = sorted(os.listdir(ODEK_SKILLS_DIR))
    fixed_headings = 0
    fixed_desc = 0
    
    for name in skills:
        skill_dir = os.path.join(ODEK_SKILLS_DIR, name)
        skill_file = os.path.join(skill_dir, 'SKILL.md')
        if not os.path.isfile(skill_file):
            continue
        
        content = read_file(skill_file)
        c1, h_changed = normalize_headings(content)
        c2, d_changed = trim_description(c1)
        
        if h_changed or d_changed:
            write_file(skill_file, c2)
            changes = []
            if h_changed:
                changes.append('headings')
            if d_changed:
                changes.append('description')
            print(f"  ✏️  {name}: {', '.join(changes)}")
            if h_changed:
                fixed_headings += 1
            if d_changed:
                fixed_desc += 1
    
    print(f"\n─── Normalization Summary ───")
    print(f"  Headings normalized: {fixed_headings}")
    print(f"  Descriptions trimmed: {fixed_desc}")


if __name__ == '__main__':
    main()
