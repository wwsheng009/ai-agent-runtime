// MessageComposer 触发菜单单测的受控宿主（非测试文件）。
// 单独放在 .tsx 里只导出组件，满足 react-refresh 的 only-export-components 约束。
import { useState } from "react";

import { MessageComposer } from "@/components/workspace/message-composer";
import { type ComposerTestProps } from "@/components/workspace/message-composer-test-support";

export function ComposerMenuHarness({ props }: { props: ComposerTestProps }) {
  const [draft, setDraft] = useState(props.draft);
  return (
    <MessageComposer
      {...props}
      draft={draft}
      onDraftChange={(next) => {
        props.onDraftChange(next);
        setDraft(next);
      }}
    />
  );
}
