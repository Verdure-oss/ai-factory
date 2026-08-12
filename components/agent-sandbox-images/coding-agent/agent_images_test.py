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

import unittest

from agent_images import (
    build_user_content,
    download_to_data_url,
    extract_image_urls,
)


class ExtractImageUrlsTest(unittest.TestCase):
    def test_empty_text(self):
        self.assertEqual(extract_image_urls(""), [])
        self.assertEqual(extract_image_urls(None), [])

    def test_markdown_image(self):
        text = "See ![screenshot](https://example.com/a.png) for details."
        self.assertEqual(extract_image_urls(text), ["https://example.com/a.png"])

    def test_markdown_image_with_alt_and_quotes(self):
        text = '![alt text]("https://example.com/b.png")'
        self.assertEqual(extract_image_urls(text), ["https://example.com/b.png"])

    def test_html_img_tag(self):
        text = '<img width="100" height="50" alt="Image" src="https://github.com/user-attachments/assets/abc123" />'
        self.assertEqual(
            extract_image_urls(text),
            ["https://github.com/user-attachments/assets/abc123"],
        )

    def test_html_single_quotes(self):
        text = "<img src='https://example.com/c.png'>"
        self.assertEqual(extract_image_urls(text), ["https://example.com/c.png"])

    def test_deduplicates_and_preserves_order(self):
        text = (
            "![a](https://example.com/x.png) and ![b](https://example.com/y.png) "
            "again ![a](https://example.com/x.png)"
        )
        self.assertEqual(
            extract_image_urls(text),
            ["https://example.com/x.png", "https://example.com/y.png"],
        )

    def test_no_images_returns_empty(self):
        text = "There is no image here, just a URL https://example.com/a.png"
        self.assertEqual(extract_image_urls(text), [])


class BuildUserContentTest(unittest.TestCase):
    def test_no_images_returns_plain_string(self):
        result = build_user_content("Work on the task.", [])
        self.assertIsInstance(result, str)
        self.assertEqual(result, "Work on the task.")

    def test_images_build_content_array(self):
        result = build_user_content(
            "Work on the task.",
            ["https://example.com/a.png", "https://example.com/b.png"],
        )
        self.assertIsInstance(result, list)
        self.assertEqual(result[0], {"type": "text", "text": "Work on the task."})
        self.assertEqual(
            result[1],
            {"type": "image_url", "image_url": {"url": "https://example.com/a.png"}},
        )
        self.assertEqual(
            result[2],
            {"type": "image_url", "image_url": {"url": "https://example.com/b.png"}},
        )

    def test_embedded_override_uses_data_url(self):
        embedded = {
            "https://example.com/a.png": "data:image/png;base64,AAAA",
            "https://example.com/b.png": None,
        }
        result = build_user_content(
            "prompt",
            ["https://example.com/a.png", "https://example.com/b.png"],
            embedded,
        )
        self.assertEqual(result[1]["image_url"]["url"], "data:image/png;base64,AAAA")
        # None keeps the original URL.
        self.assertEqual(result[2]["image_url"]["url"], "https://example.com/b.png")


class DownloadToDataUrlTest(unittest.TestCase):
    def test_invalid_url_returns_none(self):
        self.assertIsNone(download_to_data_url("not a url", timeout=1))

    def test_unreachable_host_returns_none(self):
        self.assertIsNone(
            download_to_data_url("http://127.0.0.1:1/nonexistent.png", timeout=1)
        )


if __name__ == "__main__":
    unittest.main()