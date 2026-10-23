import { memo, type ComponentProps } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import styles from "./Markdown.module.css";

// Her replies in a thread are markdown. No raw HTML is rendered, links open in a new tab, images
// become links (nothing loads from a page she read), and wide code and tables scroll sideways.

const COMPONENTS: Components = {
  a: ({ href, children }: ComponentProps<"a">) => (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  ),
  img: ({ src, alt }: ComponentProps<"img">) =>
    typeof src === "string" ? (
      <a href={src} target="_blank" rel="noopener noreferrer">
        {alt || "image"}
      </a>
    ) : null,
  pre: ({ children }: ComponentProps<"pre">) => (
    <div className={styles.scroll} tabIndex={0} role="group" aria-label="Code">
      <pre>{children}</pre>
    </div>
  ),
  table: ({ children }: ComponentProps<"table">) => (
    <div className={styles.scroll} tabIndex={0} role="group" aria-label="Table">
      <table>{children}</table>
    </div>
  ),
};

const PLUGINS = [remarkGfm];

export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return (
    <div className={styles.md}>
      <ReactMarkdown remarkPlugins={PLUGINS} components={COMPONENTS} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  );
});
