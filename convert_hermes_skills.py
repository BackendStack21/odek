#!/usr/bin/env python3
"""
Convert all Hermes Agent skills to odek SKILL.md format.
Handles nested directory structures like mlops/inference/llama-cpp/SKILL.md
"""

import os
import re
import sys

HERMES_SKILLS_DIR = os.path.expanduser("~/.hermes/skills")
ODEK_SKILLS_DIR = os.path.expanduser("~/.odek/skills")


def parse_frontmatter(text):
    """Parse YAML frontmatter between --- markers. Returns (dict, body)."""
    m = re.match(r'^---\s*\n(.*?)\n---\s*\n(.*)', text, re.DOTALL)
    if not m:
        return {}, text
    
    raw = m.group(1)
    body = m.group(2)
    
    data = {}
    current_key = None
    
    for line in raw.split('\n'):
        m2 = re.match(r'^(\w[\w_-]*)\s*:\s*(.*)', line)
        if m2 and not line.startswith(' '):
            current_key = m2.group(1)
            val = m2.group(2).strip()
            val = re.sub(r'^["\'](.*)["\']$', r'\1', val)
            
            if val == '' or val == '[]':
                data[current_key] = [] if val == '[]' else ''
            elif val.startswith('['):
                items = re.findall(r'["\']?(\w[\w\s.-]*)["\']?', val.strip('[]'))
                data[current_key] = [x.strip() for x in items if x.strip()]
            else:
                data[current_key] = val
            continue
        
        m3 = re.match(r'^\s+(\w[\w_-]*)\s*:\s*(.*)', line)
        if m3 and current_key:
            sub_key = m3.group(1)
            sub_val = m3.group(2).strip()
            sub_val = re.sub(r'^["\'](.*)["\']$', r'\1', sub_val)
            if current_key not in data:
                data[current_key] = {}
            if isinstance(data.get(current_key), dict):
                data[current_key][sub_key] = sub_val
            continue
        
        m4 = re.match(r'^\s+-\s+(.*)', line)
        if m4 and current_key:
            item = m4.group(1).strip()
            item = re.sub(r'^["\'](.*)["\']$', r'\1', item)
            if isinstance(data.get(current_key), list):
                data[current_key].append(item)
            continue
    
    return data, body.strip()


def generate_triggers(name, description, tags, category):
    """Generate odek trigger topics and actions from Hermes metadata."""
    topics = set()
    
    if isinstance(tags, list):
        for t in tags:
            t_clean = t.lower().strip()
            topics.add(t_clean)
            for part in re.split(r'[,/\s-]+', t_clean):
                if len(part) > 1:
                    topics.add(part)
    
    for part in name.replace('-', ' ').replace('_', ' ').split():
        if len(part) > 2:
            topics.add(part.lower())
    
    topics.add(category.lower())
    
    actions = set()
    desc_lower = description.lower()
    
    action_verbs = [
        'build', 'create', 'deploy', 'develop', 'test', 'run', 'debug',
        'analyze', 'audit', 'research', 'write', 'configure', 'setup',
        'install', 'manage', 'monitor', 'generate', 'convert', 'design',
        'search', 'integrate', 'optimize', 'refactor', 'review', 'fix',
        'document', 'plan', 'orchestrate', 'simulate', 'train'
    ]
    
    for verb in action_verbs:
        if verb in desc_lower:
            actions.add(verb)
    
    name_lower = name.lower()
    name_action_map = {
        'development': 'develop', 'testing': 'test', 'deployment': 'deploy',
        'debugging': 'debug', 'monitoring': 'monitor', 'audit': 'audit',
        'research': 'research', 'search': 'search', 'training': 'train',
        'generation': 'generate', 'management': 'manage', 'planning': 'plan',
        'analysis': 'analyze', 'review': 'review',
    }
    for key, verb in name_action_map.items():
        if key in name_lower:
            actions.add(verb)
    
    if not actions:
        actions.add('use')
    
    return ' '.join(sorted(topics)), ' '.join(sorted(actions))


def convert_skill(skill_path, category):
    """Convert one Hermes SKILL.md to odek format."""
    with open(skill_path, 'r') as f:
        content = f.read()
    
    front, body = parse_frontmatter(content)
    
    name = front.get('name', os.path.basename(os.path.dirname(skill_path)))
    description = front.get('description', '')
    
    tags = front.get('tags', [])
    if not tags and 'metadata' in front and isinstance(front['metadata'], dict):
        tags = front['metadata'].get('hermes', {}).get('tags', [])
    if not tags:
        tags = [category, name]
    
    topic, action = generate_triggers(name, description, tags, category)
    
    odek_front = f"""---
name: {name}
description: {description}
odek:
  trigger:
    topic: {topic}
    action: {action}
  auto_load: false
  quality: stable
---

"""
    
    body = body.strip()
    if not body.startswith('# '):
        heading = name.replace('-', ' ').replace('_', ' ').title()
        body = f"# {heading}\n\n{body}"
    
    full_content = odek_front + body
    
    skill_dir = os.path.join(ODEK_SKILLS_DIR, name)
    os.makedirs(skill_dir, exist_ok=True)
    
    out_path = os.path.join(skill_dir, 'SKILL.md')
    with open(out_path, 'w') as f:
        f.write(full_content)
    
    return name


def find_all_skills(base_dir):
    """Find all SKILL.md files at any nesting depth."""
    skills = []
    for root, dirs, files in os.walk(base_dir):
        if 'SKILL.md' in files:
            # Determine category from relative path
            rel = os.path.relpath(root, base_dir)
            parts = rel.split(os.sep)
            
            if len(parts) == 1:
                # Top-level skill: skills/name
                category = 'uncategorized'
                skill_name = parts[0]
            elif len(parts) == 2:
                # Normal: skills/category/name
                category = parts[0]
                skill_name = parts[1]
            else:
                # Deeply nested: skills/category/subcategory/name
                category = parts[0]
                skill_name = parts[-1]
            
            skill_path = os.path.join(root, 'SKILL.md')
            skills.append((skill_path, category, skill_name))
    
    return skills


def main():
    print(f"🔍 Scanning {HERMES_SKILLS_DIR} for Hermes skills...")
    
    skills_found = find_all_skills(HERMES_SKILLS_DIR)
    
    print(f"📦 Found {len(skills_found)} Hermes skills to convert")
    print()
    
    converted = 0
    errors = 0
    skipped = 0
    
    for skill_path, category, skill_name in skills_found:
        odek_path = os.path.join(ODEK_SKILLS_DIR, skill_name, 'SKILL.md')
        if os.path.isfile(odek_path):
            print(f"  ⏭️  {skill_name} (already exists)")
            skipped += 1
            continue
        
        try:
            converted_name = convert_skill(skill_path, category)
            print(f"  ✅ {converted_name}")
            converted += 1
        except Exception as e:
            print(f"  ❌ {skill_name}: {e}")
            errors += 1
    
    print()
    print(f"─── Summary ───")
    print(f"  ✅ Converted:  {converted}")
    print(f"  ⏭️  Skipped:    {skipped} (already exist)")
    print(f"  ❌ Errors:     {errors}")
    print(f"  📊 Total processed: {len(skills_found)}")


if __name__ == '__main__':
    main()
