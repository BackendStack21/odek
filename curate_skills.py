#!/usr/bin/env python3
"""
Curation pass on all 154 imported Hermes skills:
1. Remove overly generic trigger keywords causing 49 overlap groups
2. Trim descriptions > 120 chars
3. Add missing standard sections (Overview, Common Pitfalls, Verification)
4. Remove skills that are pure duplicates of odek-native skills
"""

import os
import re
import json

ODEK_SKILLS_DIR = os.path.expanduser("~/.odek/skills")

# Generic words to strip from triggers — they cause massive overlap
GENERIC_TOPICS = {
    'software-development', 'software', 'development', 'creative',
    'uncategorized', 'agent', 'devops', 'mlops', 'productivity',
    'research', 'media', 'gaming', 'social-media', 'knowledge',
    'note-taking', 'data-science', 'smart-home', 'red-teaming',
    'autonomous-ai-agents', 'apple', 'web', 'github', 'training',
    'evaluation', 'inference', 'models', 'search', 'email'
}

# Skills that are direct duplicates of native odek skills (keep odek version)
ODEK_NATIVE_DUPLICATES = {
    'requesting-code-review',  # odek already has this
    'performance-reliability-review',  # odek already has this
    'odek',  # odek's own skill about itself trumps Hermes's orchestration view
}


def read_skill(name):
    """Read a SKILL.md file and return (frontmatter_dict, body_lines, raw_body)."""
    path = os.path.join(ODEK_SKILLS_DIR, name, 'SKILL.md')
    if not os.path.isfile(path):
        return None, None, None
    
    with open(path, 'r') as f:
        content = f.read()
    
    # Split frontmatter from body
    m = re.match(r'^---\s*\n(.*?)\n---\s*\n(.*)', content, re.DOTALL)
    if not m:
        return {}, content.split('\n'), content
    
    front_raw = m.group(1)
    body = m.group(2)
    body_lines = body.split('\n')
    
    # Parse frontmatter
    front = {}
    current_key = None
    in_odek_block = False
    
    for line in front_raw.split('\n'):
        if line.strip() == 'odek:':
            current_key = 'odek'
            front['odek'] = {}
            in_odek_block = True
            continue
        
        if in_odek_block:
            if line.startswith('  '):
                # Sub-key or value under odek
                m2 = re.match(r'  (\w[\w_-]*)\s*:\s*(.*)', line)
                if m2:
                    k = m2.group(1)
                    v = m2.group(2).strip()
                    v = re.sub(r'^["\'](.*)["\']$', r'\1', v)
                    front['odek'][k] = v
                    if k == 'trigger':
                        front['odek']['trigger'] = {}
                m3 = re.match(r'    (\w[\w_-]*)\s*:\s*(.*)', line)
                if m3:
                    k = m3.group(1)
                    v = m3.group(2).strip()
                    v = re.sub(r'^["\'](.*)["\']$', r'\1', v)
                    if 'trigger' not in front['odek']:
                        front['odek']['trigger'] = {}
                    front['odek']['trigger'][k] = v
            else:
                in_odek_block = False
        
        if not in_odek_block:
            m2 = re.match(r'^(\w[\w_-]*)\s*:\s*(.*)', line)
            if m2 and not line.startswith(' '):
                k = m2.group(1)
                v = m2.group(2).strip()
                v = re.sub(r'^["\'](.*)["\']$', r'\1', v)
                if k != 'odek':
                    front[k] = v
    
    return front, body_lines, body


def write_skill(name, front, body):
    """Write a SKILL.md file from frontmatter dict and body string."""
    path = os.path.join(ODEK_SKILLS_DIR, name, 'SKILL.md')
    
    lines = ['---']
    for k, v in front.items():
        if k == 'odek':
            lines.append('odek:')
            for ok, ov in v.items():
                if ok == 'trigger':
                    lines.append('  trigger:')
                    for tk, tv in ov.items():
                        lines.append(f'    {tk}: {tv}')
                else:
                    lines.append(f'  {ok}: {ov}')
        else:
            lines.append(f'{k}: {v}')
    lines.append('---')
    lines.append('')
    
    content = '\n'.join(lines) + body
    with open(path, 'w') as f:
        f.write(content)


def fix_triggers(name, front):
    """Remove generic keywords from triggers to reduce overlap."""
    if 'odek' not in front or 'trigger' not in front['odek']:
        return False
    
    trigger = front['odek']['trigger']
    changed = False
    
    for ttype in ['topic', 'action']:
        if ttype not in trigger:
            continue
        keywords = trigger[ttype].split()
        
        # Remove generic words
        filtered = [k for k in keywords if k not in GENERIC_TOPICS]
        
        # Also remove anything that's just the category name
        # and any single-letter words
        filtered = [k for k in filtered if len(k) > 2]
        
        new_val = ' '.join(filtered)
        if new_val != trigger[ttype]:
            trigger[ttype] = new_val
            changed = True
    
    return changed


def trim_description(front):
    """Trim description to 120 chars max."""
    if 'description' not in front:
        return False
    
    desc = front['description']
    if len(desc) <= 120:
        return False
    
    # Trim at last space before 120
    trimmed = desc[:117].rsplit(' ', 1)[0] + '...'
    front['description'] = trimmed
    return True


def add_missing_sections(name, front, body_lines, body):
    """Add standard sections (Overview, Common Pitfalls, Verification) if missing."""
    body_lower = body.lower()
    changes = []
    
    # Check for Overview section
    if '## overview' not in body_lower and '## when to use' not in body_lower:
        # Add Overview right after the first heading or first paragraph
        lines = body_lines[:]
        
        # Find where to insert — after the first heading/description block
        insert_at = 0
        for i, line in enumerate(lines):
            if line.startswith('## '):
                insert_at = i
                break
            if line.strip() == '' and i > 0:
                insert_at = i + 1
        
        overview_text = [
            '## Overview',
            '',
            'Quick reference for using this skill.',
            '',
        ]
        lines[insert_at:insert_at] = overview_text
        changes.append('added ## Overview')
        body_lines = lines
    
    # Check for Common Pitfalls
    if '## common pitfalls' not in body_lower and '## pitfalls' not in body_lower:
        # Add at the end
        pitfalls = [
            '',
            '## Common Pitfalls',
            '',
            '- Add pitfalls discovered during use.',
            '',
        ]
        body_lines.extend(pitfalls)
        changes.append('added ## Common Pitfalls')
    
    # Check for Verification
    if '## verification' not in body_lower and '## verify' not in body_lower:
        verification = [
            '',
            '## Verification',
            '',
            '```bash',
            '# Add verification commands here',
            '```',
            '',
        ]
        body_lines.extend(verification)
        changes.append('added ## Verification')
    
    if changes:
        return '\n'.join(body_lines), changes
    
    return body, []


def should_remove(name):
    """Check if this skill should be removed (duplicate or low quality)."""
    # Remove native odek duplicates
    if name in ODEK_NATIVE_DUPLICATES:
        return True
    
    # Remove skills with no real content (just frontmatter)
    path = os.path.join(ODEK_SKILLS_DIR, name, 'SKILL.md')
    if os.path.isfile(path):
        with open(path, 'r') as f:
            content = f.read()
        body = re.sub(r'^---\n.*?\n---\n', '', content, flags=re.DOTALL).strip()
        if len(body) < 50:  # Practically empty
            return True
    
    return False


def main():
    # Get all skill names
    skills = sorted(os.listdir(ODEK_SKILLS_DIR))
    print(f"📊 Curating {len(skills)} skills...\n")
    
    stats = {
        'removed': 0,
        'triggers_fixed': 0,
        'descriptions_fixed': 0,
        'sections_fixed': 0,
        'unchanged': 0,
    }
    
    for name in skills:
        skill_dir = os.path.join(ODEK_SKILLS_DIR, name)
        if not os.path.isdir(skill_dir):
            continue
        
        # Check if should remove
        if should_remove(name):
            import shutil
            shutil.rmtree(skill_dir)
            print(f"  🗑️  Removed: {name}")
            stats['removed'] += 1
            continue
        
        front, body_lines, body = read_skill(name)
        if front is None:
            continue
        
        changed = False
        
        # Fix trigger overlaps
        if fix_triggers(name, front):
            changed = True
            stats['triggers_fixed'] += 1
        
        # Trim descriptions
        if trim_description(front):
            changed = True
            stats['descriptions_fixed'] += 1
        
        # Add missing sections
        new_body, section_changes = add_missing_sections(name, front, body_lines, body)
        if section_changes:
            body = new_body
            changed = True
            stats['sections_fixed'] += 1
        
        if changed:
            write_skill(name, front, body)
            print(f"  ✏️  Fixed: {name}")
            if section_changes:
                for c in section_changes:
                    print(f"       → {c}")
        else:
            stats['unchanged'] += 1
    
    print(f"\n─── Curation Summary ───")
    print(f"  🗑️  Removed (duplicates/empty):    {stats['removed']}")
    print(f"  ✏️  Trigger keywords trimmed:      {stats['triggers_fixed']}")
    print(f"  ✏️  Descriptions trimmed:          {stats['descriptions_fixed']}")
    print(f"  ✏️  Missing sections added:        {stats['sections_fixed']}")
    print(f"  ✅  Unchanged:                     {stats['unchanged']}")
    print(f"  📊 Total after curation:          {len(skills) - stats['removed']}")


if __name__ == '__main__':
    main()
