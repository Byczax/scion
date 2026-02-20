import re
import subprocess

MODULE_FILE = "MODULE.bazel"

with open(MODULE_FILE) as f:
    content = f.read()

# Regex to capture modules
pattern = r'(module\("(.+)",\s*version\s*=\s*"(\d+\.\d+\.\d+)"\))'


def get_resolved_version(module_name):
    try:
        result = subprocess.run(
            ["bazel", "query", f"@{module_name}//:all", "--output=build"],
            capture_output=True,
            text=True,
        )
        match = re.search(r'version\s*=\s*"(\d+\.\d+\.\d+)"', result.stdout)
        return match.group(1) if match else None
    except Exception:
        return None


def bump_versions(text):
    def repl(match):
        module_name = match[2]
        current_version = match[3]
        resolved_version = get_resolved_version(module_name)
        if resolved_version and resolved_version != current_version:
            print(f"Bumping {module_name}: {current_version} -> {resolved_version}")
            return f'module("{module_name}", version="{resolved_version}")'
        return match[0]

    return re.sub(pattern, repl, text)


new_content = bump_versions(content)

with open(MODULE_FILE, "w") as f:
    f.write(new_content)
