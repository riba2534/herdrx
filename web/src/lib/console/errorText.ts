/** 异常的说明文字；没有可用的说明时用 fallback。 */
export const errorText = (error: unknown, fallback: string) => (error instanceof Error && error.message ? error.message : fallback)
