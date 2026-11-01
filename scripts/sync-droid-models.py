#!/usr/bin/env python3
"""Mirror the kyo-router catalog into Factory Droid custom models.

Droid only sends reasoning_effort and inherits model prompts/tools when a
custom model maps to a built-in model. This script maps every canonical
[[model]] in config.toml to a Droid ``baseModelId`` and keeps the reasoning
default aligned with the Devin uid suffix (or the built-in default).

The built-in ids and effort sets below come from Droid 1.1.0. Re-check them
after a Droid upgrade; an unknown baseModelId is ignored by Droid and the
model silently loses its reasoning settings.
"""

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_CONFIG = ROOT / "config.toml"
DEFAULT_SETTINGS = Path.home() / ".factory" / "settings.json"

# baseModelId -> (supported efforts, default effort)
BASE = {
    "claude-fable-5": ("off low medium high xhigh max", "high"),
    "claude-fable-5.1": ("off low medium high xhigh max", "high"),
    "claude-haiku-5-5": ("off low medium high xhigh max", "medium"),
    "claude-opus-4-6": ("off low medium high max", "high"),
    "claude-opus-4-7": ("off low medium high xhigh max", "high"),
    "claude-opus-4-8": ("off low medium high xhigh max", "high"),
    "claude-opus-5": ("off low medium high xhigh max", "high"),
    "claude-opus-5-5": ("low medium high xhigh max", "medium"),
    "claude-sonnet-4-6": ("off low medium high max", "high"),
    "claude-sonnet-5": ("off low medium high xhigh max", "high"),
    "claude-sonnet-5-5": ("off low medium high xhigh max", "high"),
    "deepseek-v4.1-flash": ("off low high max", "high"),
    "deepseek-v4.1-flash-1017": ("off low high max", "high"),
    "deepseek-v4-flash-0731": ("off low high max", "high"),
    "deepseek-v4-pro": ("off low high max", "high"),
    "gemini-3.5-flash": ("minimal low medium high", "high"),
    "gemini-3.6-flash": ("low medium high", "high"),
    "gemini-3.7-flash": ("low medium high", "high"),
    "gemini-3.8-flash": ("low medium high", "high"),
    "gemini-4-argon": ("low medium high", "medium"),
    "glm-5.1": ("off high", "high"),
    "glm-5.2": ("off high max", "high"),
    "glm-5.3": ("low high max", "max"),
    "glm-5.3-flash": ("low high max", "high"),
    "gpt-5.4": ("low medium high xhigh", "medium"),
    "gpt-5.4-mini": ("low medium high xhigh", "high"),
    "gpt-5.5": ("low medium high xhigh", "medium"),
    "gpt-5.6-luna": ("none low medium high xhigh max", "medium"),
    "gpt-5.6-sol": ("none low medium high xhigh max", "medium"),
    "gpt-5.6-terra": ("none low medium high xhigh max", "medium"),
    "gpt-6.1-sol": ("low medium high xhigh max", "medium"),
    "gpt-6-astra": ("low medium high xhigh max", "medium"),
    "gpt-6-luna": ("none low medium high xhigh max", "medium"),
    "gpt-6-sol": ("none low medium high xhigh max", "medium"),
    "grok-4.6": ("low medium high xhigh", "high"),
    "grok-4.7": ("low medium high xhigh", "high"),
    "kimi-k2.6": ("off high", "high"),
    "kimi-k2.7-code": ("off high", "high"),
    "kimi-k3": ("off low high max", "high"),
    "minimax-m2.5": ("low medium high", "high"),
    "minimax-m2.7": ("high", "high"),
    "minimax-m3": ("high", "high"),
    "minimax-m3.1-flash": ("low medium high xhigh max", "max"),
    "muse-spark-1.2": ("low medium high xhigh", "high"),
    "muse-spark-1.3": ("low medium high xhigh", "high"),
    "nemotron-3-ultra": ("off high", "high"),
    "qwen3.8-max": ("low medium xhigh", "xhigh"),
}

DEVIN_RULES = [
    (r"^claude-5-fable", "claude-fable-5"),
    (r"^claude-fable-5-1", "claude-fable-5.1"),
    (r"^claude-haiku-5-5", "claude-haiku-5-5"),
    (r"^claude-opus-4-6", "claude-opus-4-6"),
    (r"^claude-opus-4-7", "claude-opus-4-7"),
    (r"^claude-opus-4-8", "claude-opus-4-8"),
    (r"^claude-opus-5-5", "claude-opus-5-5"),
    (r"^claude-opus-5", "claude-opus-5"),
    (r"^claude-sonnet-4-6", "claude-sonnet-4-6"),
    (r"^claude-sonnet-5-5", "claude-sonnet-5-5"),
    (r"^claude-sonnet-5", "claude-sonnet-5"),
    (r"^deepseek-v4-1-flash-1017", "deepseek-v4.1-flash-1017"),
    (r"^deepseek-v4-flash", "deepseek-v4-flash-0731"),
    (r"^deepseek-v4-pro", "deepseek-v4-pro"),
    (r"^gemini-3-5-flash", "gemini-3.5-flash"),
    (r"^gemini-3-6-flash", "gemini-3.6-flash"),
    (r"^gemini-3-7-flash", "gemini-3.7-flash"),
    (r"^gemini-3-8-flash", "gemini-3.8-flash"),
    (r"^gemini-4-argon", "gemini-4-argon"),
    (r"^glm-5-3-flash", "glm-5.3-flash"),
    (r"^glm-5-3", "glm-5.3"),
    (r"^glm-5-2", "glm-5.2"),
    (r"^gpt-5-4-mini", "gpt-5.4-mini"),
    (r"^gpt-5-4", "gpt-5.4"),
    (r"^gpt-5-5", "gpt-5.5"),
    (r"^gpt-5-6-luna", "gpt-5.6-luna"),
    (r"^gpt-5-6-sol", "gpt-5.6-sol"),
    (r"^gpt-5-6-terra", "gpt-5.6-terra"),
    (r"^gpt-6-1-sol", "gpt-6.1-sol"),
    (r"^gpt-6-astra", "gpt-6-astra"),
    (r"^gpt-6-luna", "gpt-6-luna"),
    (r"^gpt-6-sol", "gpt-6-sol"),
    (r"^grok-4-6", "grok-4.6"),
    (r"^grok-4-7", "grok-4.7"),
    (r"^kimi-k2-6", "kimi-k2.6"),
    (r"^kimi-k2-7", "kimi-k2.7-code"),
    (r"^kimi-k3", "kimi-k3"),
    (r"^nemotron-3-ultra", "nemotron-3-ultra"),
]

OPENCODE_RULES = [
    (r"^claude-haiku-5-5$", "claude-haiku-5-5"),
    (r"^deepseek-v4-pro$", "deepseek-v4-pro"),
    (r"^deepseek-v4\.1-flash$", "deepseek-v4.1-flash"),
    (r"^gemini-4-argon$", "gemini-4-argon"),
    (r"^glm-5\.1$", "glm-5.1"),
    (r"^glm-5\.2$", "glm-5.2"),
    (r"^glm-5\.3$", "glm-5.3"),
    (r"^glm-5\.3-flash$", "glm-5.3-flash"),
    (r"^gpt-5\.6-luna$", "gpt-5.6-luna"),
    (r"^gpt-6-luna$", "gpt-6-luna"),
    (r"^grok-4\.6$", "grok-4.6"),
    (r"^grok-4\.7$", "grok-4.7"),
    (r"^kimi-k2\.6$", "kimi-k2.6"),
    (r"^kimi-k2\.7-code$", "kimi-k2.7-code"),
    (r"^kimi-k3$", "kimi-k3"),
    (r"^minimax-m2\.5$", "minimax-m2.5"),
    (r"^minimax-m2\.7$", "minimax-m2.7"),
    (r"^minimax-m3$", "minimax-m3"),
    (r"^minimax-m3\.1-flash$", "minimax-m3.1-flash"),
    (r"^muse-spark-1\.2-contributor$", "muse-spark-1.2"),
    (r"^muse-spark-1\.3-contributor$", "muse-spark-1.3"),
    (r"^qwen3\.8-max$", "qwen3.8-max"),
]

DEAD_OPENCODE = {
    "opencode/glm-5.1",
    "opencode/grok-4.6",
    "opencode/kimi-k2.6",
    "opencode/mimo-v2.5",
    "opencode/mimo-v2.5-pro",
    "opencode/minimax-m2.5",
    "opencode/minimax-m2.7",
    "opencode/omen-alpha",
    "opencode/qwen3.6-plus",
    "opencode/qwen3.7-max",
}
LEVELS = ["none", "minimal", "low", "medium", "high", "xhigh", "max"]


def parse_models(path):
    text = path.read_text()
    models = []
    for block in re.split(r"\n(?=\[\[model\]\])", text):
        if not block.strip().startswith("[[model]]"):
            continue
        fields = {}
        for line in block.splitlines():
            match = re.match(r"\s*(\w+)\s*=\s*(.+)$", line)
            if match:
                fields[match.group(1)] = match.group(2)
        models.append(fields["id"].strip('"'))
    return models


def match_rule(rules, slug):
    for pattern, base in rules:
        if re.match(pattern, slug):
            return base
    return None


def devin_level(slug):
    found = None
    for token in slug.split("-"):
        if token in LEVELS:
            found = token
    if found:
        return found
    return "medium" if "-thinking" in slug else "off"


def build(model_id):
    provider, slug = model_id.split("/", 1)
    entry = {
        "model": model_id,
        "displayName": "kyo-router " + model_id,
        "baseUrl": "http://127.0.0.1:8080/v1",
        "apiKey": "kyo-router-local",
        "provider": "generic-chat-completion-api",
        "maxOutputTokens": 16384,
    }
    if provider == "devin":
        base = match_rule(DEVIN_RULES, slug)
        level = devin_level(slug)
        if base and level in BASE[base][0].split():
            entry["baseModelId"] = base
        entry["reasoningEffort"] = level
    elif provider == "opencode":
        base = match_rule(OPENCODE_RULES, slug)
        if base:
            entry["baseModelId"] = base
            entry["reasoningEffort"] = BASE[base][1]
        else:
            entry["reasoningEffort"] = "high"
    elif provider == "deepseek":
        base = "deepseek-v4.1-flash" if slug == "deepseek-flash" else slug
        entry["baseModelId"] = base
        entry["reasoningEffort"] = BASE[base][1]
    else:
        raise SystemExit("unknown provider: " + provider)
    return entry


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--config", type=Path, default=DEFAULT_CONFIG)
    parser.add_argument("--settings", type=Path, default=DEFAULT_SETTINGS)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    order = {"opencode": 0, "devin": 1, "deepseek": 2}
    ids = [m for m in parse_models(args.config) if m not in DEAD_OPENCODE]
    entries = sorted((build(m) for m in ids),
                     key=lambda e: (order[e["model"].split("/")[0]], e["model"]))

    settings = json.loads(args.settings.read_text())
    settings["customModels"] = entries

    counts = {p: sum(1 for e in entries if e["model"].startswith(p + "/")) for p in order}
    unmapped = [e["model"] for e in entries if "baseModelId" not in e]
    print("models:", len(entries), counts)
    print("no baseModelId:", len(unmapped))
    for model_id in unmapped:
        print("  ", model_id)

    if args.dry_run:
        return
    args.settings.write_text(json.dumps(settings, indent=2) + "\n")
    print("wrote", args.settings)


if __name__ == "__main__":
    main()
