import { fileUrl, formatSize, splitName } from "../../data/files";
import type { ThreadFile } from "../../data/model";
import { DownloadIcon, FileIcon } from "../icons";
import styles from "./threads.module.css";

// A file's name with the stem ellipsized and the extension kept.
export function FileName({ name }: { name: string }) {
  const [stem, ext] = splitName(name);
  return (
    <span className={styles.fileName} title={name}>
      <span className={styles.fileStem}>{stem}</span>
      {ext && <span className={styles.fileExt}>{ext}</span>}
    </span>
  );
}

// The files of one turn: what the owner sent (chips under their bubble) or what she shared
// (download links under her reply). Both link to the same endpoint.
export function ThreadFiles({ thread, files, direction }: { thread: string; files: readonly ThreadFile[]; direction: "in" | "out" }) {
  if (files.length === 0) return null;
  return (
    <ul className={styles.files} data-direction={direction} aria-label={direction === "in" ? "Files you sent" : "Files she shared"}>
      {files.map((f) => (
        <li key={f.id}>
          <a className={styles.fileLink} href={fileUrl(thread, f.id)} download={f.name} aria-label={`Download ${f.name}, ${formatSize(f.size)}`}>
            {direction === "in" ? <FileIcon className={styles.fileIcon} /> : <DownloadIcon className={styles.fileIcon} />}
            <FileName name={f.name} />
            <span className={styles.fileSize}>{formatSize(f.size)}</span>
          </a>
        </li>
      ))}
    </ul>
  );
}
