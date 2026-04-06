import { QuartzComponent, QuartzComponentConstructor, QuartzComponentProps } from "./types"

const RawLink: QuartzComponent = ({ fileData }: QuartzComponentProps) => {
  const slug = fileData.slug ?? ""
  const rawUrl = `/raw/${slug}.md`
  return (
    <a
      href={rawUrl}
      class="raw-link"
      title="View raw markdown"
      style={{
        opacity: 0.4,
        fontSize: "0.8em",
        textDecoration: "none",
        float: "right",
        marginTop: "-2.5em",
      }}
    >
      raw
    </a>
  )
}

export default (() => RawLink) satisfies QuartzComponentConstructor
