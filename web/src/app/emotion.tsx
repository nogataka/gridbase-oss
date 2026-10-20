"use client";
import { CacheProvider } from "@emotion/react";
import { useEmotionCache, MantineProvider } from "@mantine/core";
import { useServerInsertedHTML } from "next/navigation";

export default function RootStyleRegistry({
  children
}: {
  children: React.ReactNode
}) {
  const cache = useEmotionCache();
  cache.compat = true;
  
  useServerInsertedHTML(() => (
    <style
      data-emotion={
        `${cache.key} ${Object.keys(cache.inserted).join(" ")}`
      }
      dangerouslySetInnerHTML={{
        __html: Object.values(cache.inserted).join(" "),
      }}
    />
  ));

  return (
    <CacheProvider value={cache}>
      <MantineProvider 
        withGlobalStyles 
        withNormalizeCSS
        theme={{
          colors: {
            customColor: [
              '#e0e5f7', // ライト系 (例)
              '#c2cced',
              '#a3b2e3',
              '#8498d9',
              '#657ecc', // 通常使われる色 (明るめ)
              '#4655bf',
              '#00023e',
              '#00023e',
              '#00023e',
              '#00023e', // 一番濃い色
            ],
          },
          primaryColor: 'customColor', // カスタムカラーを指定
        }}
      
      >
        {children}
      </MantineProvider>
    </CacheProvider>
  )
}