import { useEffect, useRef, useState } from "react";
import { fileUrl, formatSize, inlineUrl, showsInline, splitName } from "../../data/files";
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

// A small square preview of a local image file. The object URL lives as long as the chip.
export function Thumb({ file }: { file: File }) {
  const ref = useRef<HTMLImageElement>(null);
  useEffect(() => {
    const img = ref.current;
    if (!img) return;
    const url = URL.createObjectURL(file);
    img.src = url;
    return () => {
      img.removeAttribute("src");
      URL.revokeObjectURL(url);
    };
  }, [file]);
  return <img ref={ref} className={styles.thumb} alt="" />;
}

// An image in the transcript: the picture itself (opens full size in a new tab) with its name,
// size and download beneath. If it won't load (gone from the lab), the plain chip takes over.
function ImageFile({ thread, file, direction, fallback }: { thread: string; file: ThreadFile; direction: "in" | "out"; fallback: React.ReactNode }) {
  const [failed, setFailed] = useState(false);
  if (failed) return <>{fallback}</>;
  return (
    <figure className={styles.imageFile} data-direction={direction}>
      <a href={inlineUrl(thread, file.id)} target="_blank" rel="noreferrer" aria-label={`Open ${file.name}`}>
        <img src={inlineUrl(thread, file.id)} alt={file.name} loading="lazy" decoding="async" onError={() => setFailed(true)} />
      </a>
      <figcaption>
        <FileName name={file.name} />
        <span className={styles.fileSize}>{formatSize(file.size)}</span>
        <a className={styles.imageDownload} href={fileUrl(thread, file.id)} download={file.name} aria-label={`Download ${file.name}, ${formatSize(file.size)}`}>
          <DownloadIcon className={styles.fileIcon} />
        </a>
      </figcaption>
    </figure>
  );
}

// The files of one turn: what the owner sent (chips under their bubble) or what she shared
// (download links under her reply). Both link to the same endpoint.
export function ThreadFiles({ thread, files, direction }: { thread: string; files: readonly ThreadFile[]; direction: "in" | "out" }) {
  if (files.length === 0) return null;
  return (
    <ul className={styles.files} data-direction={direction} aria-label={direction === "in" ? "Files you sent" : "Files she shared"}>
      {files.map((f) => {
        const chip = (
          <a className={styles.fileLink} href={fileUrl(thread, f.id)} download={f.name} aria-label={`Download ${f.name}, ${formatSize(f.size)}`}>
            {direction === "in" ? <FileIcon className={styles.fileIcon} /> : <DownloadIcon className={styles.fileIcon} />}
            <FileName name={f.name} />
            <span className={styles.fileSize}>{formatSize(f.size)}</span>
          </a>
        );
        return <li key={f.id}>{showsInline(f) ? <ImageFile thread={thread} file={f} direction={direction} fallback={chip} /> : chip}</li>;
      })}
    </ul>
  );
}
