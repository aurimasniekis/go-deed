import { Button, Code, CopyButton, Group, SimpleGrid } from '@mantine/core'
import { IconCheck, IconCopy, IconDownload } from '@tabler/icons-react'

/** One-time recovery codes with copy and download (a plain text file generated in the browser). */
export function RecoveryCodes({ codes }: { codes: string[] }) {
  const text = codes.join('\n')
  const download = () => {
    const blob = new Blob([`Deed recovery codes (each works once)\n\n${text}\n`], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'deed-recovery-codes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <>
      <SimpleGrid cols={2} spacing={6} data-testid="recovery-codes">
        {codes.map((c) => (
          <Code key={c} ta="center" fz={13}>
            {c}
          </Code>
        ))}
      </SimpleGrid>
      <Group gap="xs">
        <CopyButton value={text}>
          {({ copied, copy }) => (
            <Button
              variant="default"
              leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
              onClick={copy}
            >
              {copied ? 'Copied' : 'Copy codes'}
            </Button>
          )}
        </CopyButton>
        <Button variant="default" leftSection={<IconDownload size={14} />} onClick={download}>
          Download .txt
        </Button>
      </Group>
    </>
  )
}
