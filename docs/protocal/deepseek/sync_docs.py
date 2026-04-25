from __future__ import annotations

import re
from pathlib import Path
from urllib.parse import urlparse

import html2text
import requests
from bs4 import BeautifulSoup


ROOT = Path(__file__).resolve().parent
SITE = "https://api-docs.deepseek.com"
FETCHED_AT = "2026-04-24"

SECTIONS = [
    (
        "OpenAI 兼容 API",
        [
            "https://api-docs.deepseek.com/zh-cn/api/create-chat-completion",
            "https://api-docs.deepseek.com/zh-cn/api/create-completion",
            "https://api-docs.deepseek.com/zh-cn/api/list-models",
            "https://api-docs.deepseek.com/zh-cn/api/get-user-balance",
        ],
    ),
    (
        "DeepSeek 特性",
        [
            "https://api-docs.deepseek.com/zh-cn/guides/thinking_mode",
            "https://api-docs.deepseek.com/zh-cn/guides/multi_round_chat",
            "https://api-docs.deepseek.com/zh-cn/guides/json_mode",
            "https://api-docs.deepseek.com/zh-cn/guides/tool_calls",
            "https://api-docs.deepseek.com/zh-cn/guides/kv_cache",
            "https://api-docs.deepseek.com/zh-cn/guides/coding_agents",
        ],
    ),
    (
        "DeepSeek Beta 接口",
        [
            "https://api-docs.deepseek.com/zh-cn/guides/chat_prefix_completion",
            "https://api-docs.deepseek.com/zh-cn/guides/fim_completion",
        ],
    ),
    (
        "Anthropic 兼容",
        [
            "https://api-docs.deepseek.com/zh-cn/guides/anthropic_api",
        ],
    ),
]


def file_name(url: str) -> str:
    return f"{urlparse(url).path.rstrip('/').rsplit('/', 1)[-1]}.md"


LINK_MAP = {
    urlparse(url).path: file_name(url)
    for _, urls in SECTIONS
    for url in urls
}


def build_converter() -> html2text.HTML2Text:
    converter = html2text.HTML2Text()
    converter.body_width = 0
    converter.ignore_images = True
    converter.ignore_links = False
    converter.protect_links = True
    return converter


def absolutize_or_localize_links(node: BeautifulSoup) -> None:
    for anchor in node.select("a[href]"):
        href = anchor.get("href", "").strip()
        if not href:
            continue
        if href.startswith("#"):
            continue
        if href.startswith("/"):
            path, _, fragment = href.partition("#")
            local = LINK_MAP.get(path)
            if local:
                anchor["href"] = f"{local}#{fragment}" if fragment else local
            else:
                anchor["href"] = f"{SITE}{href}"


def clean_markdown(text: str) -> str:
    text = text.replace("\ufeff", "").replace("\u200b", "").replace("\x00", "")
    text = re.sub(r"\n[ \t]*\n[ \t]*\n+", "\n\n", text)
    text = re.sub(
        r"^# ([^\n]+)\n(?:[ \t]*\n)+ {4}([A-Z]+)\s*\n(?:[ \t]*\n)+ {4}## (/[^\n]+)\n",
        lambda m: f"# {m.group(1)}\n\n`{m.group(2)} {m.group(3)}`\n",
        text,
        count=1,
        flags=re.MULTILINE,
    )
    return text.strip() + "\n"


def fetch_doc(url: str, converter: html2text.HTML2Text) -> tuple[str, str]:
    response = requests.get(url, timeout=30)
    response.raise_for_status()
    response.encoding = "utf-8"
    soup = BeautifulSoup(response.text, "html.parser")
    content = soup.select_one("article div.theme-doc-markdown.markdown")
    if content is None:
        article = soup.find("article")
        if article is None:
            raise RuntimeError(f"Unable to locate article for {url}")
        content = article

    for hash_link in content.select("a.hash-link"):
        hash_link.decompose()

    absolutize_or_localize_links(content)

    title_node = content.find("h1")
    title = title_node.get_text(" ", strip=True) if title_node else file_name(url)
    markdown = converter.handle("".join(str(child) for child in content.contents))
    markdown = clean_markdown(markdown)
    header = [
        f"# {title}",
        "",
        f"- 来源: {url}",
        f"- 抓取日期: {FETCHED_AT}",
        "",
    ]

    body = markdown
    if markdown.startswith(f"# {title}\n"):
        body = markdown[len(f"# {title}\n") :].lstrip("\n")
    return title, "".join(line + "\n" for line in header) + body


def build_index(entries: dict[str, str]) -> str:
    lines = [
        "# DeepSeek 文档索引",
        "",
        "本目录内容抓取自 DeepSeek 官方中文文档站：`https://api-docs.deepseek.com/zh-cn/`。",
        "",
        "说明：",
        "- DeepSeek 的主 API 形态整体与 OpenAI 兼容接口接近，核心路径包括 `/chat/completions`、`/completions`、`/models` 等。",
        "- DeepSeek 也提供自身扩展能力，例如 `thinking`、`reasoning_effort`、JSON Mode、Tool Calls、KV Cache 和 Coding Agent 接入。",
        "- Beta 能力使用单独入口：`https://api.deepseek.com/beta`。",
        "- Anthropic 兼容入口使用：`https://api.deepseek.com/anthropic`。",
        "",
    ]
    for section, urls in SECTIONS:
        lines.append(f"## {section}")
        lines.append("")
        for url in urls:
            name = file_name(url)
            title = entries[name]
            lines.append(f"- [{title}]({name})")
            lines.append(f"  - 官方链接：{url}")
        lines.append("")
    lines.extend(
        [
            "## 工具",
            "",
            "- [同步脚本](sync_docs.py)",
            "",
        ]
    )
    return "\n".join(lines).rstrip() + "\n"


def main() -> None:
    converter = build_converter()
    titles: dict[str, str] = {}
    for _, urls in SECTIONS:
        for url in urls:
            name = file_name(url)
            title, markdown = fetch_doc(url, converter)
            titles[name] = title
            (ROOT / name).write_text(markdown, encoding="utf-8")
    (ROOT / "index.md").write_text(build_index(titles), encoding="utf-8")


if __name__ == "__main__":
    main()
