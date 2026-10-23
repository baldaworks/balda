import { existsSync } from "node:fs";
import { posix } from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitepress";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const repositoryOnlyDirectories = ["architecture", "superpowers"];
const repositoryOnlyPages = [
  "balda.md",
  "reference/backoffice-ui-review.md",
  "reference/backoffice-webauthn-dependency-review.md",
  "reference/command-runtime.md",
  "reference/internals.md",
  "reference/messaging.md",
  "reference/session-memory.md",
];

export default defineConfig({
  lang: "en-US",
  title: "Balda",
  description: "User documentation for the Balda engineering agent.",
  cleanUrls: true,
  lastUpdated: true,
  // Engineering contracts remain available in the repository.
  srcExclude: [
    ...repositoryOnlyDirectories.map((directory) => `${directory}/**`),
    ...repositoryOnlyPages,
  ],
  markdown: {
    config(md) {
      // Inline examples may contain Go template syntax, which is literal text.
      const codeInline = md.renderer.rules.code_inline!;
      md.renderer.rules.code_inline = (...args) =>
        codeInline(...args).replace(/^<code\b/, "<code v-pre");

      // Keep repository Markdown intact while linking excluded pages on GitHub.
      const linkOpen = md.renderer.rules.link_open!;
      md.renderer.rules.link_open = (tokens, index, options, env, self) => {
        const token = tokens[index];
        const href = token.attrGet("href");
        if (href && !/^(?:[a-z][a-z\d+.-]*:|\/\/|#)/i.test(href)) {
          const [, pathname, suffix = ""] = href.match(/^([^?#]*)(.*)$/)!;
          let page = posix.normalize(
            pathname.startsWith("/")
              ? `docs${pathname}`
              : posix.join("docs", posix.dirname(env.relativePath), pathname),
          );
          if (page.endsWith("/")) page += "index.md";
          else if (page.endsWith(".html")) page = page.slice(0, -5) + ".md";
          else if (!posix.extname(page)) page += ".md";

          const docsPage = page.replace(/^docs\//, "");
          const repositoryOnly =
            !page.startsWith("docs/") ||
            repositoryOnlyPages.includes(docsPage) ||
            repositoryOnlyDirectories.some((directory) =>
              docsPage.startsWith(`${directory}/`),
            );
          if (
            repositoryOnly &&
            page.endsWith(".md") &&
            !page.startsWith("../") &&
            existsSync(posix.join(repositoryRoot, page))
          ) {
            token.attrSet(
              "href",
              `https://github.com/baldaworks/balda/blob/main/${page}${suffix}`,
            );
          }
        }
        return linkOpen(tokens, index, options, env, self);
      };
    },
  },
  themeConfig: {
    nav: [
      { text: "Features", link: "/features/" },
      { text: "Guides", link: "/guides/auto-mode" },
      { text: "Commands", link: "/commands" },
      { text: "Reference", link: "/reference/configuration" },
    ],
    sidebar: [
      {
        text: "Features",
        items: [
          { text: "Overview", link: "/features/" },
          { text: "Auto mode", link: "/features/auto-mode" },
        ],
      },
      {
        text: "Guides",
        items: [
          { text: "Get started", link: "/reference/onboarding" },
          { text: "Use Auto mode", link: "/guides/auto-mode" },
          { text: "Telegram formatting", link: "/telegram-formatting" },
          { text: "Zulip setup", link: "/zulip-webhook" },
          { text: "Slack setup", link: "/slack" },
          { text: "Mattermost setup", link: "/mattermost" },
        ],
      },
      {
        text: "Reference",
        items: [
          { text: "Chat commands", link: "/commands" },
          { text: "Configuration", link: "/reference/configuration" },
          { text: "State database", link: "/reference/database" },
          { text: "Skills", link: "/reference/skills" },
          { text: "Plugins", link: "/reference/plugins" },
        ],
      },
    ],
    socialLinks: [
      { icon: "github", link: "https://github.com/baldaworks/balda" },
    ],
    search: {
      provider: "local",
    },
  },
});
