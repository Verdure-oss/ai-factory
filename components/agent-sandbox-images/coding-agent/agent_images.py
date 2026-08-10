# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Helpers for attaching issue images to the model request.

Issue bodies reference images either as Markdown links (``![alt](url)``) or as
HTML ``<img src="...">`` tags. By default these URLs stay embedded in the task
prompt as plain text, which a multimodal model cannot "see". This module
extracts those URLs and builds a structured user message with one
``image_url`` content block per image so the model can actually read them.
"""

import base64
import re
import urllib.error
import urllib.request

# Markdown image links: ![alt](url). The URL may be a bare token or a quoted
# value; we exclude character-class syntax like trailing punctuation.
_MD_IMAGE_RE = re.compile(r"!\[[^\]]*\]\(\s*(?:['\"])?([^'\")\s]+)[^)]*\)")
# HTML <img src="..."> tags (GitHub issue forms render images this way).
_HTML_IMG_RE = re.compile(r"<img\b[^>]*\bsrc\s*=\s*['\"]([^'\"]+)['\"]", re.IGNORECASE)

_USER_AGENT = "ai-factory-agent/1.0"


def extract_image_urls(text):
    """Return the unique image URLs referenced in issue markdown/HTML text.

    Order is preserved on first appearance; duplicates are dropped. Empty
    text yields an empty list.
    """
    seen = set()
    urls = []
    for pattern in (_MD_IMAGE_RE, _HTML_IMG_RE):
        for match in pattern.finditer(text or ""):
            url = match.group(1).strip()
            if url and url not in seen:
                seen.add(url)
                urls.append(url)
    return urls


def build_user_content(prompt, image_urls, embedded=None):
    """Build the ``user`` message content for the chat request.

    Returns the plain ``prompt`` string when there are no images, preserving
    the original text-only behavior. When images are present, returns a
    content array with a leading text block followed by one ``image_url``
    block per image.

    ``embedded`` optionally maps an image URL to a data URL (or ``None`` to
    keep the original URL). This lets a caller substitute images that were
    downloaded in the sandbox, falling back to the raw URL when a download
    failed or a URL must be passed through.
    """
    if not image_urls:
        return prompt
    content = [{"type": "text", "text": prompt}]
    for url in image_urls:
        data_url = None
        if isinstance(embedded, dict):
            data_url = embedded.get(url)
        content.append(
            {"type": "image_url", "image_url": {"url": data_url or url}}
        )
    return content


def download_to_data_url(url, timeout=15):
    """Download an image and return a ``data:`` URL, or ``None`` on failure.

    Uses ``urllib`` so the sandbox proxy environment (http_proxy/https_proxy)
    is honored. Returns ``None`` for any transport or decoding error so a
    caller can fall back to the original URL.
    """
    try:
        request = urllib.request.Request(url, headers={"User-Agent": _USER_AGENT})
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
        content_type = response.headers.get("Content-Type", "") or "image/png"
        encoded = base64.b64encode(raw).decode("ascii")
        return "data:%s;base64,%s" % (content_type, encoded)
    except (urllib.error.URLError, OSError, ValueError):
        return None